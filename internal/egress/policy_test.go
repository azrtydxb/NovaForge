package egress_test

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"testing"

	"github.com/novaforge/novaforge/internal/egress"
)

func TestApprovedDestinationPolicy(t *testing.T) {
	p, err := egress.Parse(`[{"scheme":"http","host":"169.254.169.254","port":80,"cidrs":["169.254.0.0/16"]},{"scheme":"http","host":"127.0.0.1","port":80,"cidrs":["127.0.0.0/8"]},{"scheme":"http","host":"192.168.10.10","port":80,"cidrs":["0.0.0.0/0"]}]`)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"http://169.254.169.254/", "http://127.0.0.1/", "http://192.168.10.10/", "https://unapproved.example/", "http://user:secret@127.0.0.1/"} {
		if _, _, err := p.Resolve(context.Background(), raw); err == nil {
			t.Fatalf("allowed %s", raw)
		}
	}
}

func TestApprovedMinIOConnection(t *testing.T) {
	endpoint := os.Getenv("TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("TEST_S3_ENDPOINT not set")
	}
	u, err := url.Parse("http://" + endpoint)
	if err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		t.Fatal("fixture requires a literal cluster endpoint")
	}
	bits := 32
	if ip.To4() == nil {
		bits = 128
	}
	p, err := egress.Parse(fmt.Sprintf(`[{"scheme":"http","host":%q,"port":%s,"cidrs":[%q]}]`, host, port, host+"/"+strconv.Itoa(bits)))
	if err != nil {
		t.Fatal(err)
	}
	// A poisoned ambient proxy must not redirect the approved request.
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	resp, err := p.Client().Get(u.String() + "/minio/health/live")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("health status %d", resp.StatusCode)
	}
	cfg, err := p.GitConfig(context.Background(), u.String())
	if err != nil || len(cfg) == 0 {
		t.Fatalf("Git pin: %v %v", cfg, err)
	}
	if _, _, err := p.Resolve(context.Background(), "https://"+endpoint); err == nil {
		t.Fatal("allowed unapproved scheme")
	}
}
