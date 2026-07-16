package main

import (
	"context"
	"os"
	"os/signal"
	"strconv"
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

	source, err := ingress.NewRedisSource(ctx, redisClient, "synx:inbox", "agent_awake", consumerName())
	if err != nil {
		panic(err)
	}

	agentReg := registry.NewAgentRegistry(ctx, redisClient)
	defer agentReg.Close()

	var reg registry.Registry = agentReg

	memory := agent.NewMemory(redisClient)
	consumer := ingress.NewConsumer(source, reg)
	if err := consumer.Start(ctx, *memory, ingress.LoadConfig()); err != nil {
		panic(err)
	}
}

func consumerName() string {
	if name := os.Getenv("AVM_CONSUMER"); name != "" {
		return name
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "avm"
	}
	return host + "-" + strconv.Itoa(os.Getpid())
}
