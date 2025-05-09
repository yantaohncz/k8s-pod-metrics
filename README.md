# Kubernetes Metrics Proxy and Registrar to Consul

这是一个 Go 程序，可以在两种模式下运行：

1.  **代理模式 (`proxy`)**: 暴露一个 HTTP 端口 (默认为 8080)，用于代理对 Kubernetes Node 上 cAdvisor 指标端点 (`/metrics/cadvisor`) 的访问。这允许通过一个集中的服务访问 Node 指标。
2.  **注册器模式 (`registrar`)**: 发现 Kubernetes 集群中带有特定 Prometheus 注释的 Pod 和所有 Node，并将它们的指标端点注册到 Consul，以便 Prometheus 可以发现并采集这些指标。

## 功能

- 连接到 Kubernetes 集群。
- **代理模式**:
    - 代理对 Node cAdvisor 指标的访问 (路径如 `/api/v1/nodes/<node_ip>:10250/metrics/cadvisor`)。
    - 监听 HTTP 端口 (默认为 8080)。
- **注册器模式**:
    - 发现带有 `prometheus.io/scrape: "true"` 注释的 Pod。
    - 从 Pod 注释中提取 `prometheus.io/path` 和 `prometheus.io/port`。
    - 发现集群中的所有 Node。
    - 从环境变量获取集群名称 (`CLUSTER_NAME`)。
    - 从环境变量获取可选的 `SHARD_PATHS`，用于为服务添加 `shard_path` 元数据。
    - **在注册服务前，会先从 Consul Agent 获取已存在的服务列表，如果服务已存在，则跳过注册，避免重复。**
    - 将 Pod 的指标信息（服务名称 `pod--<namespace>--<pod_name>`、IP、端口、指标路径、集群名称、命名空间、`shard_path` (如果配置)）注册到 Consul。
    - 将 Node 的指标信息（服务名称 `node--<node_ip>`、Node IP 作为地址、端口 `31112` (代理服务暴露的 NodePort)、指标路径 `/api/v1/nodes/<node_ip>:10250/metrics/cadvisor` (通过代理访问)、集群名称、`shard_path` (如果配置)）注册到 Consul。
    - 定期检查 Consul 中注册的服务，如果对应的 Pod 或 Node 在 Kubernetes 中不存在，则从 Consul 中反注册该服务。
- 需要 Service Account 权限来访问 Kubernetes API。

## 如何使用

### 本地运行

在本地运行程序需要访问 Kubernetes 集群和 Consul 实例。

1.  确保已安装 Go 环境。
2.  克隆项目仓库。
3.  设置以下环境变量：
    - `MODE`: 设置为 `proxy` 或 `registrar`。
    - `KUBECONFIG`: (可选，如果不在集群内运行) 指向你的 Kubernetes 配置文件路径。
    - `CLUSTER_NAME`: 当前 Kubernetes 集群的名称 (主要用于 `registrar` 模式)。
    - `CONSUL_ADDRESS`: Consul 代理的地址（例如 `localhost:8500`）。
    - `SYNC_PERIOD`: (仅限 `registrar` 模式) 程序运行周期，以秒为单位 (例如 `60`)。
    - `SHARD_PATHS`: (可选, 仅限 `registrar` 模式) 逗号分隔的 shard 路径列表，例如 `shard-1,shard-2`。程序会随机选择一个路径作为服务的 `shard_path` 元数据。
4.  在项目根目录运行以下命令：

    ```bash
    go run main.go
    ```

### 部署到 Kubernetes

推荐将程序打包成 Docker 镜像并部署到 Kubernetes 集群中。通常情况下，`registrar` 和 `proxy` 模式会作为两个独立的 Deployment 进行部署。

1.  **创建 Service Account 和 Role Binding**

    程序需要访问 Kubernetes API 来获取 Pod 和 Node 信息。创建一个 Service Account 并赋予其必要的权限。创建一个名为 `rbac.yaml` 的文件，并将以下内容复制到该文件中：

    ```yaml
    apiVersion: v1
    kind: ServiceAccount
    metadata:
      name: k8s-consul-registrar-sa
      namespace: default # 根据需要修改命名空间
    ---
    apiVersion: rbac.authorization.k8s.io/v1
    kind: ClusterRole
    metadata:
      name: k8s-consul-registrar-cr
    rules:
    - apiGroups: [""] # "" 表示核心 API 组
      resources: ["pods", "nodes"]
      verbs: ["list", "get", "watch"]
    ---
    apiVersion: rbac.authorization.k8s.io/v1
    kind: ClusterRoleBinding
    metadata:
      name: k8s-consul-registrar-crb
    subjects:
    - kind: ServiceAccount
      name: k8s-consul-registrar-sa
      namespace: default # 根据需要修改命名空间
    roleRef:
      kind: ClusterRole
      name: k8s-consul-registrar-cr
      apiGroup: rbac.authorization.k8s.io
    ```

    应用此配置：

    ```bash
    kubectl apply -f rbac.yaml
    ```

2.  **创建 Dockerfile**

    创建一个名为 `Dockerfile` 的文件，并将以下内容复制到该文件中：

    ```dockerfile
    FROM golang:1.24-alpine AS builder

    WORKDIR /app

    COPY go.mod go.sum ./
    RUN go mod download

    COPY . .

    # 如果 go.mod 中的模块名与项目目录名不一致，请取消注释并修改下面的行
    # RUN go mod init k8s-pod-metrics 
    RUN go build -ldflags="-s -w" -o k8s-pod-metrics main.go

    FROM alpine:latest

    WORKDIR /app

    COPY --from=builder /app/k8s-pod-metrics .

    # 默认环境变量 (可以在 Deployment 中覆盖)
    ENV MODE="registrar"
    ENV CLUSTER_NAME="default-cluster"
    ENV CONSUL_ADDRESS="127.0.0.1:8500" # registrar 模式通常连接到 sidecar 或本地 agent
    ENV SYNC_PERIOD="60"

    CMD ["/app/k8s-pod-metrics"]
    ```

3.  **构建 Docker 镜像**

    在包含 `Dockerfile` 和 Go 源代码的目录中运行以下命令：

    ```bash
    docker build -t your-repo/k8s-pod-metrics:latest .
    ```
    (将 `your-repo/k8s-pod-metrics:latest` 替换为你的实际镜像名称和标签)

4.  **将 Docker 镜像推送到镜像仓库**

    将构建好的 Docker 镜像推送到你选择的镜像仓库。

    ```bash
    docker push your-repo/k8s-pod-metrics:latest
    ```

5.  **创建 Registrar Deployment (`deployment.yaml`)**

    此 Deployment 运行 `registrar` 模式的程序，并通常包含一个 Consul Agent sidecar。

    ```yaml
    apiVersion: apps/v1
    kind: Deployment
    metadata:
      name: k8s-consul-registrar
      namespace: default
    spec:
      replicas: 1
      selector:
        matchLabels:
          app: k8s-consul-registrar
      template:
        metadata:
          labels:
            app: k8s-consul-registrar
        spec:
          serviceAccountName: k8s-consul-registrar-sa
          containers:
          - name: consul-agent # Consul Agent Sidecar
            image: consul:1.7.3 # 或其他兼容版本
            args:
            - "agent"
            - "-client=0.0.0.0"
            # 根据你的 Consul 集群配置调整以下参数
            # - "-datacenter=your-dc"
            # - "-join=consul-server-address"
            ports:
            - containerPort: 8500
              name: http
            # readinessProbe 和 livenessProbe 示例
            readinessProbe:
              tcpSocket:
                port: 8500
              initialDelaySeconds: 10
              periodSeconds: 5
            livenessProbe:
              tcpSocket:
                port: 8500
              initialDelaySeconds: 30
              periodSeconds: 10

          - name: k8s-consul-registrar
            image: your-repo/k8s-pod-metrics:latest # 替换为你的镜像
            imagePullPolicy: Always
            env:
            - name: MODE
              value: "registrar"
            - name: CLUSTER_NAME
              value: "your-cluster-name" # 替换为你的集群名称
            - name: CONSUL_ADDRESS
              value: "127.0.0.1:8500" # 连接到同 Pod 内的 Consul Agent
            - name: SYNC_PERIOD
              value: "60" # 同步周期（秒）
            - name: SHARD_PATHS # 可选
              value: "shard-1,shard-2,shard-3"
          restartPolicy: Always
    ```
    **注意**: 上述 `deployment.yaml` 中的 Consul Agent 配置是一个基本示例，你可能需要根据你的 Consul 集群设置（如 datacenter, join address 等）进行调整。

6.  **创建 Proxy Deployment 和 Service (`deployment-svc.yaml`)**

    此 Deployment 运行 `proxy` 模式的程序，并通过 Service 暴露。

    ```yaml
    apiVersion: apps/v1
    kind: Deployment
    metadata:
      name: k8s-consul-registrar-svc # Proxy Deployment 名称
      namespace: default
    spec:
      replicas: 1 # 根据需要调整副本数
      selector:
        matchLabels:
          app: k8s-consul-registrar-svc
      template:
        metadata:
          labels:
            app: k8s-consul-registrar-svc
        spec:
          serviceAccountName: k8s-consul-registrar-sa
          containers:
          - name: k8s-consul-registrar-svc
            image: your-repo/k8s-pod-metrics:latest # 替换为你的镜像
            imagePullPolicy: Always
            env:
            - name: MODE
              value: "proxy"
            # CLUSTER_NAME 和 CONSUL_ADDRESS 在 proxy 模式下通常不是必需的
            # 但如果你的 proxy 实现未来需要它们，可以在此添加
            ports:
            - containerPort: 8080 # 程序在 proxy 模式下监听的端口
          restartPolicy: Always
    ---
    apiVersion: v1
    kind: Service
    metadata:
      name: k8s-consul-registrar-proxy-service # Proxy Service 名称
      namespace: default
    spec:
      selector:
        app: k8s-consul-registrar-svc # 选择 Proxy Deployment 的 Pod
      type: NodePort # 或 LoadBalancer，根据你的环境选择
      ports:
        - name: http-proxy
          protocol: TCP
          port: 8080       # Service 内部端口
          targetPort: 8080 # Pod 容器的目标端口
          nodePort: 31112  # NodePort (如果 type 为 NodePort)
    ```

7.  **应用 Kubernetes 配置**

    分别应用你的 RBAC, Registrar Deployment, Proxy Deployment 和 Proxy Service 配置：

    ```bash
    kubectl apply -f rbac.yaml
    kubectl apply -f deployment.yaml       # Registrar Deployment
    kubectl apply -f deployment-svc.yaml   # Proxy Deployment and Service
    ```

## 配置

可以通过以下环境变量配置程序：

- `MODE`: 设置为 `proxy` 或 `registrar`，决定程序运行的模式。
- `CLUSTER_NAME`: (主要用于 `registrar` 模式) 当前 Kubernetes 集群的名称。用于在 Consul 中标记服务。
- `CONSUL_ADDRESS`: Consul 代理的地址。
    - 在 `registrar` 模式下，如果与 Consul Agent sidecar 一起部署，通常设置为 `127.0.0.1:8500`。
    - 在 `proxy` 模式下，通常不需要此变量。
- `SYNC_PERIOD`: (仅限 `registrar` 模式) 程序运行周期，以秒为单位。默认为 `600` 秒，如果未设置或无效。
- `SHARD_PATHS`: (可选, 仅限 `registrar` 模式) 逗号分隔的 shard 路径列表。如果提供，程序会随机选择一个路径作为服务的 `shard_path` 元数据。

在 Kubernetes 中部署时，这些变量在各自的 Deployment YAML 文件中配置。
