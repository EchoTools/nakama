package service

import (
	"context"
	"fmt"

	"github.com/cloudflare/cloudflare-go/v3"
	"github.com/cloudflare/cloudflare-go/v3/ips"
)

func main() {
	client := cloudflare.NewClient()
	ips, err := client.IPs.List(context.TODO(), ips.IPListParams{})
	if err != nil {
		panic(err.Error())
	}
	fmt.Printf("%+v\n", ips)
}
