package main

import (
	"flag"
	"log"
	"net"
	"time"

	"market-simulator/exchange/gen/market"
	"market-simulator/exchange/internal/server"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

func main() {
	listenAddr := flag.String("listen", ":50051", "gRPC listen address")
	tickMs := flag.Int("tick-ms", 200, "server tick duration in ms")
	seedFile := flag.String("seed-file", "", "path to JSON seed file for initial agent accounts")
	flag.Parse()

	listener, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		log.Fatalf("failed to listen on %s: %v", *listenAddr, err)
	}

	grpcServer := grpc.NewServer()
	exchangeServer := server.NewExchangeServer(time.Duration(*tickMs) * time.Millisecond)

	if *seedFile != "" {
		if err := exchangeServer.LoadSeedFile(*seedFile); err != nil {
			log.Fatalf("seed file error: %v", err)
		}
		log.Printf("loaded seed file: %s", *seedFile)
	}

	market.RegisterExchangeServiceServer(grpcServer, exchangeServer)

	// Enable server reflection for local dev tooling.
	reflection.Register(grpcServer)

	log.Printf("exchange gRPC server listening on %s", *listenAddr)
	if err := grpcServer.Serve(listener); err != nil {
		log.Fatalf("gRPC server stopped: %v", err)
	}
}
