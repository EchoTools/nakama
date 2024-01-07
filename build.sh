set -x

GOWORK=off CGO_ENABLED=1 CGO_CFLAGS="-O0 -g" go build -trimpath -mod=vendor -gcflags "-trimpath $PWD" -gcflags="all=-N -l" -asmflags "-trimpath $PWD" -ldflags "-s -X main.version=3.20.0 -X main.commitID=`git rev-parse --short HEAD`" -o nakama
#GOWORK=off CGO_ENABLED=1 CGO_CFLAGS="-O0 -g" go build -C evrbackend -trimpath -mod=vendor -gcflags "-trimpath $PWD/evrbackend" -gcflags="all=-N -l" -asmflags "-trimpath $PWD/evrbackend" -ldflags "-s -X main.version=3.20.0 -X main.commitID=`git rev-parse --short HEAD`" --buildmode=plugin -o nakama -o ../data/modules/backend.so ./backend.go

