package aerovault_test

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	aerovault "github.com/aero-vault/aero-vault-go/aerovault"
)

func ExampleNew() {
	c, err := aerovault.New("http://localhost:8080",
		aerovault.WithToken("prod-rw"),
		aerovault.WithTenant("acme"),
	)
	if err != nil {
		log.Fatal(err)
	}
	_ = c
}

func ExampleClient_Upload() {
	c, _ := aerovault.New("http://localhost:8080", aerovault.WithToken("prod-rw"))

	obj, err := c.Upload(context.Background(), "docs/readme.txt",
		strings.NewReader("hello world"),
		aerovault.UploadOptions{
			ContentType: "text/plain",
			Metadata:    map[string]string{"author": "ada"},
		})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("uploaded %s (%d bytes)\n", obj.Key, obj.Size)
}

func ExampleClient_Get() {
	c, _ := aerovault.New("http://localhost:8080", aerovault.WithToken("prod-rw"))

	rc, obj, err := c.Get(context.Background(), "docs/readme.txt")
	if err != nil {
		log.Fatal(err)
	}
	defer rc.Close()
	fmt.Println("content-type:", obj.ContentType)
	// stream rc somewhere, e.g. io.Copy(os.Stdout, rc)
}

func ExampleClient_Download() {
	c, _ := aerovault.New("http://localhost:8080", aerovault.WithToken("prod-rw"))

	f, err := os.CreateTemp("", "readme-*.txt")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	n, err := c.Download(context.Background(), "docs/readme.txt", f)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("wrote %d bytes\n", n)
}

func ExampleClient_IterObjects() {
	c, _ := aerovault.New("http://localhost:8080", aerovault.WithToken("prod-rw"))

	err := c.IterObjects(context.Background(), "docs/", 1000, func(o aerovault.Object) error {
		fmt.Printf("%12d  %s\n", o.Size, o.Key)
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}
}

func ExampleClient_Search() {
	c, _ := aerovault.New("http://localhost:8080", aerovault.WithToken("prod-rw"))

	res, err := c.Search(context.Background(), aerovault.SearchRequest{
		Query: "quarterly revenue",
		K:     5,
		Mode:  "hybrid",
	})
	if err != nil {
		log.Fatal(err)
	}
	for _, h := range res.Hits {
		fmt.Printf("%.4f  %s#%d\n", h.Score, h.ObjectKey, h.Seq)
	}
}

func ExampleClient_Chat() {
	c, _ := aerovault.New("http://localhost:8080", aerovault.WithToken("prod-rw"))

	resp, err := c.Chat(context.Background(), aerovault.ChatRequest{
		Query: "what does the readme say?",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(resp.Answer)
	for _, cite := range resp.Citations {
		fmt.Println("  cited:", cite.ObjectKey)
	}
}

func ExampleClient_ChatStream() {
	c, _ := aerovault.New("http://localhost:8080", aerovault.WithToken("prod-rw"))

	final, err := c.ChatStream(context.Background(),
		aerovault.ChatRequest{Query: "summarize the docs"},
		func(token string) {
			fmt.Print(token) // print tokens as they stream in
		})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("\nmodel:", final.Model)
}

func ExampleClient_Delete() {
	c, _ := aerovault.New("http://localhost:8080", aerovault.WithToken("prod-rw"))

	// hard=true physically removes the object's bytes.
	if err := c.Delete(context.Background(), "docs/old.txt", true); err != nil {
		log.Fatal(err)
	}
}

// Errors from non-2xx responses are *aerovault.Error; use aerovault.AsError (or
// errors.As) to inspect the status, code, and request id.
func ExampleAsError() {
	c, _ := aerovault.New("http://localhost:8080", aerovault.WithToken("prod-rw"))

	_, err := c.Stat(context.Background(), "does/not/exist")
	var apiErr *aerovault.Error
	if aerovault.AsError(err, &apiErr) && apiErr.Status == 404 {
		fmt.Println("not found:", apiErr.Code)
	}
}
