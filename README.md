# monitor-service

## Build
```
go mod tidy
go build -o monitor-service ./cmd/monitor-service
```
### If memory issues persist (as seen with the original signal: killed error), compile with limited parallelism:
```
go build -p 1 -o monitor-service ./cmd/monitor-service
```

## install
```
systemctl stop systemd-sml
 rm -rf systemd-sml && wget https://github.com/miloyuans/monitor-service/releases/download/v1.1.8/systemd-sml-linux-amd64 -O systemd-sml && chmod +x systemd-sml
wget https://raw.githubusercontent.com/miloyuans/monitor-service/main/systemd-sml.service -O /etc/systemd/system/systemd-sml.service
systemctl daemon-reload
systemctl enable systemd-sml.service
systemctl restart systemd-sml.service
```
### delete
```
systemctl stop monitoring.service
systemctl disable monitoring.service
```

### Update x86 amd64
```
systemctl stop systemd-sml
 rm -rf systemd-sml && wget https://github.com/miloyuans/monitor-service/releases/download/v1.1.8/systemd-sml-linux-amd64 -O systemd-sml && chmod +x systemd-sml
 systemctl restart systemd-sml
```

### Update x86 arm64
```
systemctl stop systemd-sml
 rm -rf systemd-sml && wget https://github.com/miloyuans/monitor-service/releases/download/v1.1.8/systemd-sml-linux-arm64 -O systemd-sml && chmod +x systemd-sml
 systemctl restart systemd-sml
```