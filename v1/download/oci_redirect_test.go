//go:build !opa_no_oci

package download

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/open-policy-agent/opa/v1/keys"
	"github.com/open-policy-agent/opa/v1/plugins/rest"
)

func TestOCITargetCrossHostRedirect(t *testing.T) {
	blob := []byte("bundle-bytes")
	dgst := digest.FromBytes(blob)

	for _, code := range []int{
		http.StatusMovedPermanently,
		http.StatusFound,
		http.StatusSeeOther,
		http.StatusTemporaryRedirect,
		http.StatusPermanentRedirect,
	} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			// Behaves like S3 with a presigned URL: rejects a second auth mechanism.
			storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if a := r.Header.Get("Authorization"); a != "" {
					http.Error(w, "InvalidArgument: Only one auth mechanism allowed; got "+a, http.StatusBadRequest)
					return
				}
				_, _ = w.Write(blob)
			}))
			defer storage.Close()

			registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") == "" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				http.Redirect(w, r, storage.URL+"/blob?X-Amz-Signature=abc", code)
			}))
			defer registry.Close()

			restConf := fmt.Sprintf(`{"url": %q, "type": "oci",
				"credentials": {"bearer": {"token": "AWS:secret", "scheme": "Basic"}}}`, registry.URL)
			client, err := rest.New([]byte(restConf), map[string]*keys.Config{})
			if err != nil {
				t.Fatal(err)
			}
			plugin, err := client.Config().AuthPlugin(client.AuthPluginLookup())
			if err != nil {
				t.Fatal(err)
			}
			target, err := newOCITarget(plugin, client.Config(), strings.TrimPrefix(registry.URL, "http://")+"/org/repo:1.0.0")
			if err != nil {
				t.Fatal(err)
			}

			rc, err := target.Fetch(t.Context(), ocispec.Descriptor{
				MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
				Digest:    dgst,
				Size:      int64(len(blob)),
			})
			if err != nil {
				t.Fatalf("fetch after %d redirect: %v", code, err)
			}
			defer rc.Close()
			got, err := io.ReadAll(rc)
			if err != nil {
				t.Fatalf("read after %d redirect: %v", code, err)
			}
			if !bytes.Equal(got, blob) {
				t.Fatalf("got %q", got)
			}
		})
	}
}
