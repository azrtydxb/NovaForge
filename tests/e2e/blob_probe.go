//go:build ignore

// Read-only physical inventory for the Git-host acceptance suite.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/blobstore"
)

func main() {
	org := flag.String("org", "", "")
	repo := flag.String("repo", "", "")
	want := flag.Int("want", 0, "")
	flag.Parse()
	if _, err := uuid.Parse(*org); err != nil {
		panic(err)
	}
	if _, err := uuid.Parse(*repo); err != nil {
		panic(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client, err := blobstore.New(ctx, blobstore.Options{Endpoint: os.Getenv("S3_ENDPOINT"), AccessKey: os.Getenv("S3_ACCESS_KEY"), SecretKey: os.Getenv("S3_SECRET_KEY"), Bucket: "novaforge-releases"})
	if err != nil {
		panic(err)
	}
	objects, err := client.ListPage(ctx, "org/"+*org+"/repo/"+*repo+"/", "", 1000)
	if err != nil {
		panic(err)
	}
	if len(objects) != *want {
		fmt.Fprintf(os.Stderr, "physical object count %d, want %d\n", len(objects), *want)
		os.Exit(1)
	}
	fmt.Printf("physical object count=%d\n", len(objects))
}
