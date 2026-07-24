package main

import (
	"context"
	"log"
	"os"

	"right-signin/internal/app"
	"right-signin/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	runner, err := app.NewRunner(cfg)
	if err != nil {
		log.Fatalf("初始化 runner 失败: %v", err)
	}

	if err := runner.Run(context.Background()); err != nil {
		log.Printf("执行失败: %v", err)
		os.Exit(1)
	}
}
