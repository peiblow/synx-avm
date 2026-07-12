package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/joho/godotenv"
	"github.com/peiblow/avm/agent"
	"github.com/peiblow/avm/database"
	"github.com/peiblow/avm/ingress"
	"github.com/peiblow/avm/registry"
)

func main() {
	_ = godotenv.Load()

	redisClient, err := database.NewRedisClient()
	if err != nil {
		panic(err)
	}
	defer redisClient.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	source, err := ingress.NewRedisSource(ctx, redisClient, "synx:inbox", "agent_awake", "consumer1")
	if err != nil {
		panic(err)
	}

	licenses, err := registry.LoadLicenses()
	if err != nil {
		panic(err)
	}

	agentReg := registry.NewAgentRegistry(ctx, os.Getenv("MCP_URL"), licenses, redisClient)
	defer agentReg.Close()

	var reg registry.Registry = agentReg

	memory := agent.NewMemory(redisClient)
	consumer := ingress.NewConsumer(source, reg)
	if err := consumer.Start(ctx, *memory); err != nil {
		panic(err)
	}
}
