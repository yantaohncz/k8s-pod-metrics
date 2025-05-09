#对podip可以ping通的机器进行consul集群搭建
docker run -d \
  --name=consul \
  -p 8500:8500 \
  -p 8600:8600/udp \
  -p 8300:8300 \
  -p 8301:8301/udp \
  -p 8302:8302 \
  -p 8301:8301 \
  -p 8302:8302/udp \
  -v $(pwd)/consul-data:/consul/data \
registry-itwork.yonghui.cn/base/consul:1.7.3 consul agent \
    -server \
    -bootstrap-expect=1 \
    -ui \
    -node=consul \
    -client=0.0.0.0 \
    -data-dir=/consul/data 



#对podip不可以ping通的机器进行consul集群搭建
docker run -d \
  --name=consul \
  -v $(pwd)/consul-data:/consul/data \
  --network=host \
registry-itwork.yonghui.cn/base/consul:1.7.3 consul agent \
    -server \
    -bootstrap-expect=1 \
    -ui \
    -node=consul \
    -bind=10.210.48.53 \
    -client=0.0.0.0 \
    -data-dir=/consul/data 



#   docker run -d \
#   --name grafana \
#   -p 3000:3000 \
#   -e "GF_DATABASE_TYPE=mysql" \
#   -e "GF_DATABASE_HOST=10.67.82.119:3306" \
#   -e "GF_DATABASE_NAME=grafana" \
#   -e "GF_DATABASE_USER=root" \
#   -e "GF_DATABASE_PASSWORD=your_password" \
#   grafana/grafana



# docker run -d --name mysql57 \
#   -e MYSQL_ROOT_PASSWORD=your_password \
#   -p 3306:3306 \
#   registry-itwork.yonghui.cn/base/mysql:5.7
