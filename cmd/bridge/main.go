package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/signal"
	"syscall"

	"github.com/anthropics/remote-relay/internal/bridge"
	"github.com/anthropics/remote-relay/internal/protocol"
	qrcode "github.com/skip2/go-qrcode"
)

func main() {
	relay := flag.String("relay", "", "Relay server URL (e.g. ws://localhost:8080 or wss://relay.example.com)")
	target := flag.String("target", "http://localhost:4096", "Local Claude Code server URL")
	password := flag.String("password", "", "Claude Code server password for Basic auth (optional)")
	flag.Parse()

	if *relay == "" {
		fmt.Fprintln(os.Stderr, "Error: --relay flag is required")
		fmt.Fprintln(os.Stderr, "\nUsage:")
		flag.PrintDefaults()
		os.Exit(1)
	}

	relayHost, err := extractHost(*relay)
	if err != nil {
		log.Fatalf("invalid relay URL %q: %v", *relay, err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	client := bridge.NewRelayClient(*relay)
	if err := client.Connect(ctx); err != nil {
		log.Fatalf("failed to connect to relay: %v", err)
	}

	roomCode := client.RoomCode()
	kx := client.KeyExchange()
	publicKey := kx.PublicKeyEncoded()

	qrPayload := fmt.Sprintf("https://%s/r/%s?pk=%s", relayHost, roomCode, publicKey)
	qr, err := qrcode.New(qrPayload, qrcode.Medium)
	if err != nil {
		log.Fatalf("failed to generate QR code: %v", err)
	}
	fmt.Println(qr.ToSmallString(false))

	fmt.Printf("Room:   %s\n", roomCode)
	fmt.Printf("Relay:  %s\n", *relay)
	fmt.Printf("Target: %s\n\n", *target)
	fmt.Println("Waiting for phone to connect...")

	if err := client.WaitForPeer(ctx); err != nil {
		if ctx.Err() != nil {
			log.Println("Shutdown requested before peer connected.")
			return
		}
		log.Fatalf("key exchange failed: %v", err)
	}

	fmt.Printf("Connected! Proxying to %s\n", *target)

	var pwPtr *string
	if *password != "" {
		pwPtr = password
	}
	proxy := bridge.NewHTTPProxy(*target, pwPtr)
	sseBridge := bridge.NewSSEBridge(*target, pwPtr)

	defer func() {
		log.Println("Disconnecting...")
		sseBridge.Unsubscribe()
		if err := client.Close(); err != nil {
			log.Printf("error closing relay connection: %v", err)
		}
	}()

routing:
	for {
		plaintext, err := client.Receive(ctx)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			log.Printf("receive error: %v", err)
			break
		}

		msg, err := protocol.ParseMessage(plaintext)
		if err != nil {
			log.Printf("failed to parse message: %v", err)
			continue
		}

		switch m := msg.(type) {
		case protocol.RequestMessage:
			resp, err := proxy.HandleRequest(m)
			if err != nil {
				log.Printf("proxy error for request %s: %v", m.ID, err)
				continue
			}
			responseJSON, err := json.Marshal(resp)
			if err != nil {
				log.Printf("failed to marshal response for request %s: %v", m.ID, err)
				continue
			}
			if err := client.Send(responseJSON); err != nil {
				log.Printf("failed to send response for request %s: %v", m.ID, err)
				if ctx.Err() != nil {
					break routing
				}
			}

		case protocol.SSESubscribeMessage:
			if err := sseBridge.Subscribe(m.Path, func(event protocol.SSEEventMessage) {
				eventJSON, err := json.Marshal(event)
				if err != nil {
					log.Printf("failed to marshal SSE event: %v", err)
					return
				}
				if err := client.Send(eventJSON); err != nil {
					log.Printf("failed to forward SSE event: %v", err)
				}
			}); err != nil {
				log.Printf("failed to subscribe to SSE at %s: %v", m.Path, err)
			}

		case protocol.SSEUnsubscribeMessage:
			_ = m
			sseBridge.Unsubscribe()

		default:
			log.Printf("warning: unknown message type %T, ignoring", m)
		}
	}
}

func extractHost(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("no host found in URL %q", rawURL)
	}
	return parsed.Host, nil
}
