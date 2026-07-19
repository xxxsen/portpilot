package config

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func TestManagersSerializeConcurrentRevisionUpdates(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "clients.json")
	initial := ClientFile{
		Revision: 9,
		ClientConfig: []ClientConfig{{
			ClientID: "client", ClientToken: testToken, Enabled: true,
			ProxyList: []ProxyEnvelope{testProxy("proxy", "tcp", "127.0.0.1:17010")},
		}},
	}
	writeTestJSON(t, path, initial)
	first, err := NewManager(path, "127.0.0.1:15100", "127.0.0.1:16100")
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewManager(path, "127.0.0.1:15100", "127.0.0.1:16100")
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for index, manager := range []*Manager{first, second} {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			next := Redacted(manager.Snapshot())
			next.ClientConfig[0].ProxyList[0].Remark = []string{"first", "second"}[index]
			_, replaceErr := manager.Replace(9, next, nil)
			results <- replaceErr
		}()
	}
	close(start)
	workers.Wait()
	close(results)

	successes := 0
	conflicts := 0
	for result := range results {
		switch {
		case result == nil:
			successes++
		case errors.Is(result, ErrRevisionConflict):
			conflicts++
		default:
			t.Fatalf("unexpected replace error: %v", result)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("unexpected results: successes=%d conflicts=%d", successes, conflicts)
	}
	onDisk, err := LoadClientFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if onDisk.Revision != 10 || onDisk.ClientConfig[0].ClientToken != testToken {
		t.Fatalf("concurrent update corrupted configuration: %#v", onDisk)
	}
}
