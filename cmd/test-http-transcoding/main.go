package main

import (
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"github.com/jpillora/backoff"
)

type config struct {
	Attempts          int           `short:"a" help:"Retry attempts. Default: unlimited" default:"-1"`
	URL               string        `short:"u" help:"URL to test." default:"http://127.0.0.1/api/rguide.RouteGuide/GetFeature"`
	Host              string        `short:"H" help:"Host field to set in header" default:"routeguide.local"`
	Dur               time.Duration `short:"d" help:"Duration to wait between attempts" default:"5s"`
	ContinueOnSuccess bool          `short:"c" help:"Continue on success until timeout"`
	Body              string        `short:"B" help:"Request Body" default:"{}"`

	PollUntilError bool `short:"v" help:"Keep polling until error or timeout, otherwise keep polling until success or timeout."`
	ErrorThreshold uint `short:"e" help:"Allow for X errors during polling"`

	url *url.URL
	req *http.Request
}

func main() {
	cfg := &config{}
	_ = kong.Parse(cfg)
	b := &backoff.Backoff{
		Min:    cfg.Dur,
		Max:    cfg.Dur,
		Factor: 1,
	}

	var err error
	cfg.url, err = url.Parse(cfg.URL)
	if err != nil {
		log.Println("cannot parse url", cfg.URL)
		os.Exit(1)
	}

	cfg.req = &http.Request{
		Method: http.MethodPost,
		URL:    cfg.url,
		Body:   io.NopCloser(strings.NewReader(cfg.Body)),
		Host:   cfg.Host,
	}

	if !cfg.PollUntilError {
		runUntilSuccess(cfg, b)
	} else {
		runUntilFail(cfg, b)
	}
}

func runUntilSuccess(cfg *config, b *backoff.Backoff) {
	for {
		resp, err := http.DefaultClient.Do(cfg.req)
		if err == nil && resp.StatusCode == 200 {
			log.Println("SUCCESS")
			_ = resp.Body.Close()
			if !cfg.ContinueOnSuccess {
				os.Exit(0)
			}
		}
		if err != nil {
			log.Println("error", err)
		}
		if resp != nil {
			b, _ := io.ReadAll(resp.Body)
			log.Printf("req %s response %q status: %d\n", cfg.url, b, resp.StatusCode)
			_ = resp.Body.Close()
		}
		dur := b.Duration()
		if b.Attempt() >= float64(cfg.Attempts) && cfg.Attempts >= 0 {
			log.Println("timed out")
			os.Exit(1)
		}
		time.Sleep(dur)
	}
}

func runUntilFail(cfg *config, b *backoff.Backoff) {
	http.DefaultClient.Timeout = time.Minute
	var errCnt uint
	for {
		resp, err := http.DefaultClient.Do(cfg.req)
		if err == nil && resp.StatusCode == 200 {
			log.Println("ok")
			dur := b.Duration()
			if b.Attempt() >= float64(cfg.Attempts) && cfg.Attempts >= 0 {
				os.Exit(0)
			}
			_ = resp.Body.Close()
			time.Sleep(dur)
			continue
		}
		if err != nil {
			log.Println("error", err)
		}
		if resp != nil {
			b, _ := io.ReadAll(resp.Body)
			log.Printf("req %s response %q status: %d\n", cfg.url, b, resp.StatusCode)
			_ = resp.Body.Close()
		}
		if errCnt < cfg.ErrorThreshold {
			errCnt++
			time.Sleep(b.Duration())
			continue
		}
		os.Exit(1)
	}
}
