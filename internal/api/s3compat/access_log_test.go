package s3compat

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestBucketAccessLoggingWritesTargetObjects(t *testing.T) {
	srv := newTestServer(t)
	base := srv.URL

	if resp, _ := do(t, "PUT", base+"/target", nil, nil); resp.StatusCode != 200 {
		t.Fatalf("create target status=%d", resp.StatusCode)
	}
	if resp, _ := do(t, "PUT", base+"/source/object.txt", []byte("payload"), nil); resp.StatusCode != 200 {
		t.Fatalf("put source status=%d", resp.StatusCode)
	}
	config, err := xml.Marshal(bucketLoggingStatus{
		Logging: loggingEnabled{TargetBucket: "target", TargetPrefix: "audit/"},
	})
	if err != nil {
		t.Fatalf("marshal logging config: %v", err)
	}
	if resp, _ := do(t, "PUT", base+"/source?logging", config, nil); resp.StatusCode != 200 {
		t.Fatalf("put logging status=%d", resp.StatusCode)
	}

	if resp, body := do(t, "GET", base+"/source/object.txt", nil, map[string]string{"User-Agent": "access-log-test"}); resp.StatusCode != 200 || string(body) != "payload" {
		t.Fatalf("get source status=%d body=%q", resp.StatusCode, body)
	}
	resp, body := do(t, "GET", base+"/target?list-type=2", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list target status=%d body=%s", resp.StatusCode, body)
	}
	var result listBucketResult
	if err := xml.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode target listing: %v body=%s", err, body)
	}
	if len(result.Contents) < 2 {
		t.Fatalf("target listing has %d log objects, want at least 2: %s", len(result.Contents), body)
	}
	found := false
	for _, item := range result.Contents {
		if !strings.HasPrefix(item.Key, "audit/source/") {
			continue
		}
		resp, logBody := do(t, "GET", base+"/target/"+item.Key, nil, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("get log %q status=%d", item.Key, resp.StatusCode)
		}
		if strings.Contains(string(logBody), "GET.OBJECT") && strings.Contains(string(logBody), "object.txt") && strings.Contains(string(logBody), "access-log-test") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("target listing did not contain source GET access log: %s", body)
	}
}
