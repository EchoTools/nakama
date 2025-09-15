FROM golang:1.25
WORKDIR /app
COPY . .
RUN cd backend/module && go build -buildmode=plugin -trimpath -o ../../data/modules/backend.so ./main.go
RUN go build -o nakama ./main.go
