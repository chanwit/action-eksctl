CGO_ENABLED=0 go build -o provisioner ./main.go
docker build -t chanwit/action-eksctl .
docker push chanwit/action-eksctl

docker tag chanwit/action-eksctl docker push chanwit/action-eksctl:$1

docker push chanwit/action-eksctl:$1