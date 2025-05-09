package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"path/filepath"

	consulapi "github.com/hashicorp/consul/api"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

// 从指定文件读取 Token
func readTokenFromFile(filePath string) (string, error) {
	tokenData, err := ioutil.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	return string(tokenData), nil
}

func main() {
	mode := os.Getenv("MODE")
	if mode == "" {
		fmt.Println("MODE environment variable not set, defaulting to 'registrar'")
		mode = "registrar" // Default mode
	}

	// Consul 地址
	consulAddress := os.Getenv("CONSUL_ADDRESS")
	if consulAddress == "" {
		consulAddress = "localhost:8500" // 默认 Consul 地址
		fmt.Println("CONSUL_ADDRESS environment variable not set, using default: localhost:8500")
	}

	// 创建 Consul 客户端
	consulConfig := consulapi.DefaultConfig()
	consulConfig.Address = consulAddress
	consulClient, err := consulapi.NewClient(consulConfig)
	if err != nil {
		fmt.Printf("Error creating Consul client: %v\n", err)
		return
	}

	// Kubernetes 配置
	var config *rest.Config
	// 尝试使用 In-cluster Config
	config, err = rest.InClusterConfig()
	if err != nil {
		// 如果 In-cluster Config 失败，尝试使用 Kubeconfig
		fmt.Printf("In-cluster config failed: %v, trying kubeconfig...\n", err)
		kubeconfigPath := filepath.Join(homedir.HomeDir(), ".kube", "config")
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		if err != nil {
			fmt.Printf("Error building kubeconfig: %v\n", err)
			return
		}
	}

	// 创建 Kubernetes 客户端
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		fmt.Printf("Error creating clientset: %v\n", err)
		return
	}

	if mode == "proxy" {
		// 启动 HTTP 服务
		go func() {
			http.HandleFunc("/api/v1/nodes/", func(w http.ResponseWriter, r *http.Request) {
				segments := strings.Split(r.URL.Path, "/")
				if len(segments) < 5 {
					http.Error(w, "Invalid URL", http.StatusBadRequest)
					return
				}
				nodeIPPort := segments[4]
				parts := strings.Split(nodeIPPort, ":")
				if len(parts) != 2 {
					http.Error(w, "Invalid Node IP:Port format", http.StatusBadRequest)
					return
				}
				nodeIP := parts[0]
				//nodePort := parts[1]

				targetURL := fmt.Sprintf("https://%s:10250/metrics/cadvisor", nodeIP)
				remote, err := url.Parse(targetURL)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}

				// 创建自定义的传输层配置，忽略证书验证
				transport := &http.Transport{
					TLSClientConfig: &tls.Config{
						InsecureSkipVerify: true,
					},
				}

				proxy := httputil.NewSingleHostReverseProxy(remote)
				proxy.Transport = transport // 应用自定义传输层配置
				proxy.Director = func(req *http.Request) {
					req.Header.Add("X-Forwarded-Host", req.Host)
					req.Header.Add("X-Origin-Host", remote.Host)
					req.URL.Scheme = remote.Scheme
					req.URL.Host = remote.Host
					req.URL.Path = "/metrics/cadvisor" // 确保请求的路径正确
					// 从文件读取 Token
					tokenFilePath := "/var/run/secrets/kubernetes.io/serviceaccount/token"
					token, err := readTokenFromFile(tokenFilePath)
					if err != nil {
						http.Error(w, fmt.Sprintf("Failed to read token: %v", err), http.StatusInternalServerError)
						return
					}
					// 添加 Authorization 请求头
					req.Header.Set("Authorization", "Bearer "+token)
				}

				proxy.ServeHTTP(w, r)
			})
			fmt.Println("Starting HTTP server on port 8080")
			http.ListenAndServe(":8080", nil)
		}()
	} else if mode == "registrar" {
		// 注册 Node 指标到 Consul
		go func() {
			// 获取同步周期，默认为 600 秒
			syncPeriodStr := os.Getenv("SYNC_PERIOD")
			syncPeriod, err := strconv.Atoi(syncPeriodStr)
			if err != nil || syncPeriod <= 0 {
				fmt.Println("Invalid or missing SYNC_PERIOD environment variable, defaulting to 600 seconds")
				syncPeriod = 600
			}
			period := time.Duration(syncPeriod) * time.Second

			for { // 无限循环
				fmt.Println("Starting Consul registration and deregistration cycle...")

				// --- 获取 Consul 中已存在的服务 ---
				fmt.Println("Fetching existing services from Consul...")
				existingServicesMap, err := consulClient.Agent().Services()
				if err != nil {
					fmt.Printf("Error fetching services from Consul: %v. Skipping existence check for this cycle.\n", err)
					// 如果获取失败，创建一个空 map 避免后续 nil panic，并继续执行，但不会跳过任何注册
					existingServicesMap = make(map[string]*consulapi.AgentService)
				}
				existingServiceNames := make(map[string]bool)
				for serviceID := range existingServicesMap {
					existingServiceNames[serviceID] = true
				}
				fmt.Printf("Found %d existing services in Consul agent.\n", len(existingServiceNames))
				// --- 获取 Consul 中已存在的服务结束 ---

				// 获取所有 Node
				nodes, err := clientset.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
				if err != nil {
					fmt.Printf("Error listing nodes: %v\n", err)
				} else {
					for _, node := range nodes.Items {
						nodeIP := ""
						for _, address := range node.Status.Addresses {
							if address.Type == "InternalIP" {
								nodeIP = address.Address
								break
							}
						}
						if nodeIP == "" {
							fmt.Printf("Could not find InternalIP for node %s\n", node.Name)
							continue
						}

						// 获取集群名称
						clusterName := os.Getenv("CLUSTER_NAME")
						if clusterName == "" {
							fmt.Println("CLUSTER_NAME environment variable not set")
							clusterName = "cluster-test"
						}

						// 注册到 Consul
						// 使用 node-- 作为前缀
						serviceName := "node--" + nodeIP

						meta := map[string]string{
							"cluster_name": clusterName,
							"metrics_ip":   nodeIP,
							"metrics_port": "31112",
							"metrics_path": "/api/v1/nodes/" + nodeIP + ":10250/metrics/cadvisor", // 使用代理地址
						}

						// 读取环境变量 SHARD_PATHS
						shardPaths := os.Getenv("SHARD_PATHS")
						var shardPath string // 声明 shardPath 变量
						if shardPaths != "" {
							// 分割字符串
							shardPathArray := strings.Split(shardPaths, ",")
							// 随机取值
							if len(shardPathArray) > 0 {
								randomIndex := time.Now().UnixNano() % int64(len(shardPathArray))
								shardPath = shardPathArray[randomIndex]
							}
						}

						// 添加 shard_path 到 metadata
						if shardPath != "" {
							meta["shard_path"] = shardPath
						}

						registration := &consulapi.AgentServiceRegistration{
							ID:      serviceName,
							Name:    serviceName,
							Address: nodeIP,
							Port:    31112, // 统一使用 31112 端口
							Meta:    meta,
							Tags:    []string{serviceName},
						}

						// 检查服务是否已存在
						if _, exists := existingServiceNames[registration.ID]; exists {
							// fmt.Printf("Node service %s already exists in Consul, skipping registration.\n", registration.ID)
						} else {
							err = consulClient.Agent().ServiceRegister(registration)
							if err != nil {
								fmt.Printf("Error registering node service %s with Consul: %v\n", registration.ID, err)
							} else {
								fmt.Printf("Successfully registered node service %s with Consul\n", registration.ID)
								existingServiceNames[registration.ID] = true // 添加到 map 中，防止同一周期内重复添加（虽然理论上 ID 唯一）
							}
						}
					}
				}

				// 反注册不存在的 Node
				services, err := consulClient.Agent().Services()
				if err != nil {
					fmt.Printf("Error retrieving services from Consul: %v\n", err)
				} else {
					// 获取 Kubernetes 集群中的所有 Node
					nodes, err := clientset.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
					if err != nil {
						fmt.Printf("Error listing nodes: %v\n", err)
					} else {
						// 创建一个 map 用于存储 Kubernetes 中的 Node IP
						nodeIPMap := make(map[string]bool)
						for _, node := range nodes.Items {
							for _, address := range node.Status.Addresses {
								if address.Type == "InternalIP" {
									nodeIPMap[address.Address] = true
									break
								}
							}
						}

						// 遍历 Consul 中的 Node 服务，检查其对应的 Node 是否仍然存在于 Kubernetes 集群中
						for serviceID, service := range services {
							// 检查是否是 Node 服务 (使用 node-- 前缀)
							if strings.HasPrefix(service.ID, "node--") {
								nodeIP := strings.TrimPrefix(service.ID, "node--")
								clusterName := os.Getenv("CLUSTER_NAME")
								if clusterName == "" {
									fmt.Println("CLUSTER_NAME environment variable not set")
									clusterName = "cluster-test"
								}
								if cn, ok := service.Meta["cluster_name"]; ok && cn == clusterName {
									// 如果 Consul 中的 Node 服务在 Kubernetes 集群中不存在，则从 Consul 中反注册该服务
									if _, ok := nodeIPMap[nodeIP]; !ok {
										err = consulClient.Agent().ServiceDeregister(serviceID)
										if err != nil {
											fmt.Printf("Error deregistering service %s from Consul: %v\n", serviceID, err)
										} else {
											fmt.Printf("Successfully deregistered service %s from Consul\n", serviceID)
										}
									}
								}
							}
						}
					}
				}

				// 2. 列出所有 Pod
				pods, err := clientset.CoreV1().Pods("").List(context.TODO(), metav1.ListOptions{})
				if err != nil {
					fmt.Printf("Error listing pods: %v\n", err)
				} else {
					fmt.Printf("There are %d pods in the cluster\n", len(pods.Items))

					// 3. 筛选 Pod 并提取元数据
					for _, pod := range pods.Items {
						if pod.ObjectMeta.Annotations["prometheus.io/scrape"] == "true" {
							metricsPath := pod.ObjectMeta.Annotations["prometheus.io/path"]
							metricsPort := pod.ObjectMeta.Annotations["prometheus.io/port"]

							// 读取环境变量 SHARD_PATHS
							shardPaths := os.Getenv("SHARD_PATHS")
							var shardPath string // 声明 shardPath 变量
							if shardPaths != "" {
								// 分割字符串
								shardPathArray := strings.Split(shardPaths, ",")
								// 随机取值
								if len(shardPathArray) > 0 {
									randomIndex := time.Now().UnixNano() % int64(len(shardPathArray))
									shardPath = shardPathArray[randomIndex]
								}
							}

							fmt.Printf("Pod: %s, Metrics Path: %s, Metrics Port: %s\n", pod.Name, metricsPath, metricsPort)

							// 4. 获取集群名称
							clusterName := os.Getenv("CLUSTER_NAME")
							if clusterName == "" {
								fmt.Println("CLUSTER_NAME environment variable not set")
								clusterName = "cluster-test"
							}

							if metricsPath == "" {
								fmt.Println("metricsPath environment variable not set")
								metricsPath = "/metrics"
							}

							// 5. 注册到 Consul
							// 使用 pod-- 作为前缀，并使用 -- 作为命名空间和 Pod 名称的分隔符
							serviceName := "pod--" + pod.Namespace + "--" + pod.Name

							port, err := strconv.Atoi(metricsPort)
							if err != nil {
								fmt.Printf("Error converting metricsPort to integer: %v\n", err)
								continue
							}

							meta := map[string]string{
								"metrics_path": metricsPath,
								"cluster_name": clusterName,
								"metric_ip":    pod.Status.PodIP,
								"pod_ip":       pod.Status.PodIP,
								"pod_name":     pod.Name,
								"instance":     pod.Status.PodIP + ":" + metricsPort,
								"metrics_port": metricsPort,
								"namespace":    pod.Namespace,
							}

							// 添加 shard_path 到 metadata
							if shardPath != "" {
								meta["shard_path"] = shardPath
							}

							registration := &consulapi.AgentServiceRegistration{
								ID:      serviceName,
								Name:    serviceName,
								Port:    port,
								Meta:    meta,
								Address: pod.Status.PodIP,
								Tags:    []string{serviceName},
							}

							// 检查服务是否已存在
							if _, exists := existingServiceNames[registration.ID]; exists {
								// fmt.Printf("Pod service %s already exists in Consul, skipping registration.\n", registration.ID)
							} else {
								err = consulClient.Agent().ServiceRegister(registration)
								if err != nil {
									fmt.Printf("Error registering pod service %s with Consul: %v\n", registration.ID, err)
								} else {
									fmt.Printf("Successfully registered pod service %s with Consul\n", registration.ID)
									existingServiceNames[registration.ID] = true // 添加到 map 中
								}
							}
						}
					}

					// 6. 反注册不存在的 Pod
					services, err := consulClient.Agent().Services()
					if err != nil {
						fmt.Printf("Error retrieving services from Consul: %v\n", err)
					} else {
						for serviceID, service := range services {
							// 检查是否是 Pod 服务 (使用 pod-- 前缀)
							if strings.HasPrefix(service.ID, "pod--") {
								// 解析 serviceID 获取 namespace 和 podName
								// 使用 -- 作为命名空间和 Pod 名称的分隔符
								parts := strings.SplitN(service.ID[5:], "--", 2) // 注意这里是 service.ID[5:] 因为前缀是 "pod--"
								if len(parts) != 2 {
									fmt.Printf("Warning: Invalid pod service ID format in Consul: %s\n", service.ID)
									continue // 跳过格式不正确的服务
								}
								namespace := parts[0]
								podName := parts[1]

								// 检查 metadata 中的 namespace 是否一致 (可选，但更健壮)
								if ns, ok := service.Meta["namespace"]; ok && ns != namespace {
									fmt.Printf("Warning: Namespace mismatch for service ID %s: metadata has %s, parsed from ID has %s\n", service.ID, ns, namespace)
									// 可以选择在这里跳过或使用 metadata 中的 namespace，这里选择使用解析出的 namespace
								}
								clusterName := os.Getenv("CLUSTER_NAME")
								if clusterName == "" {
									fmt.Println("CLUSTER_NAME environment variable not set")
									clusterName = "cluster-test"
								}
								if cn, ok := service.Meta["cluster_name"]; ok && cn == clusterName {

									_, err := clientset.CoreV1().Pods(namespace).Get(context.TODO(), podName, metav1.GetOptions{})
									if err != nil {
										err = consulClient.Agent().ServiceDeregister(serviceID)
										if err != nil {
											fmt.Printf("Error deregistering service %s from Consul: %v\n", serviceID, err)
										} else {
											fmt.Printf("Successfully deregistered service %s from Consul\n", serviceID)
										}
									}
								}
							}
						}
					}
				}

				fmt.Printf("Consul registration and deregistration cycle finished. Sleeping for %d seconds...\n", syncPeriod)
				time.Sleep(period) // 周期性等待
			}
		}()
	} else {
		fmt.Printf("Unknown MODE: %s. Please set MODE to 'proxy' or 'registrar'.\n", mode)
		return // 如果模式未知，则退出
	}

	// 保持主进程运行，以便选定的 goroutine 可以继续运行
	select {}
}
