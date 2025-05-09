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
  consul:1.7.3 consul agent \
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
  consul:1.7.3 consul agent \
    -server \
    -bootstrap-expect=1 \
    -ui \
    -node=consul \
    -bind=10.210.48.53 \
    -client=0.0.0.0 \
    -data-dir=/consul/data 




