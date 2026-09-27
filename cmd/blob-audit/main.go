// blob-audit reports historical repository payloads without a live reference.
// It never deletes objects. Explicit --enqueue adds candidates to the ordinary
// collector, which rechecks live references before deleting exact keys.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/novaforge/novaforge/internal/blobstore"
	"github.com/novaforge/novaforge/internal/database"
)

func main() {
	org := flag.String("org", "", "required organization UUID (original physical key owner)")
	after := flag.String("after", "", "resume after this object key")
	limit := flag.Int("limit", 1000, "maximum keys to inspect, up to 10000")
	grace := flag.Duration("grace", 24*time.Hour, "minimum object age, at least 24h")
	enqueue := flag.Bool("enqueue", false, "enqueue reported candidates; no direct deletion")
	flag.Parse()
	id, err := uuid.Parse(*org)
	if err != nil || id == uuid.Nil || *grace < 24*time.Hour {
		log.Fatal("--org UUID and --grace >=24h are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := database.Connect(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	blobs, err := blobstore.New(ctx, blobstore.Options{Endpoint: os.Getenv("S3_ENDPOINT"), AccessKey: os.Getenv("S3_ACCESS_KEY"), SecretKey: os.Getenv("S3_SECRET_KEY"), Bucket: os.Getenv("S3_BUCKET"), UseSSL: os.Getenv("S3_USE_SSL") == "true"})
	if err != nil {
		log.Fatal(err)
	}
	prefix := "org/" + id.String() + "/repo/"
	if *after != "" && !strings.HasPrefix(*after, prefix) {
		log.Fatal("--after must be in the selected organization prefix")
	}
	objects, err := blobs.ListPage(ctx, prefix, *after, *limit)
	if err != nil {
		log.Fatal(err)
	}
	enc := json.NewEncoder(os.Stdout)
	candidates := 0
	for _, obj := range objects {
		parts := strings.Split(obj.Key, "/")
		if len(parts) < 6 || (parts[4] != "lfs" && parts[4] != "release") || obj.Modified.After(time.Now().Add(-*grace)) {
			continue
		}
		if _, err := uuid.Parse(parts[3]); err != nil {
			continue
		}
		var exists bool
		// The physical key may remain referenced by a transferred repository. This
		// maintenance check returns existence only, never another owner's payload.
		err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM gitplatform.lfs_objects WHERE blob_key=$1) OR EXISTS(SELECT 1 FROM gitplatform.release_assets WHERE blob_key=$1) OR EXISTS(SELECT 1 FROM gitplatform.blob_cleanup WHERE blob_key=$1)`, obj.Key).Scan(&exists)
		if err != nil {
			log.Fatal(err)
		}
		if exists {
			continue
		}
		if *enqueue {
			if _, err = pool.Exec(ctx, `INSERT INTO gitplatform.blob_cleanup(blob_key,org_id) VALUES($1,$2) ON CONFLICT(blob_key) DO NOTHING`, obj.Key, id); err != nil {
				log.Fatal(err)
			}
		}
		if err = enc.Encode(obj); err != nil {
			log.Fatal(err)
		}
		candidates++
	}
	next := ""
	if len(objects) > 0 {
		next = objects[len(objects)-1].Key
	}
	fmt.Fprintf(os.Stderr, "inspected=%d candidates=%d enqueue=%t next_after=%q\n", len(objects), candidates, *enqueue, next)
}
