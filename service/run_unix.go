//go:build !windows

package service

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

// Run 在控制台或用户级服务中运行，并在收到终止信号时等待清理完成。
func Run(run func(context.Context) error) error {
	if run == nil {
		return fmt.Errorf("代理服务运行函数不能为空")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return run(ctx)
}
