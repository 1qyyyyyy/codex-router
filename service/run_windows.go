//go:build windows

package service

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"golang.org/x/sys/windows/svc"
)

const serviceName = "CodexRouter"

// Run 在 Windows 服务管理器或控制台中运行代理。
func Run(run func(context.Context) error) error {
	if run == nil {
		return fmt.Errorf("代理服务运行函数不能为空")
	}
	isService, err := svc.IsWindowsService()
	if err != nil {
		return fmt.Errorf("无法判断 Windows 服务状态：%w", err)
	}
	if !isService {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return run(ctx)
	}
	return svc.Run(serviceName, &serviceHandler{run: run})
}

type serviceHandler struct{ run func(context.Context) error }

func (h *serviceHandler) Execute(_ []string, requests <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	var once sync.Once
	finished := make(chan error, 1)
	go func() { finished <- h.run(ctx) }()
	changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case req := <-requests:
			switch req.Cmd {
			case svc.Interrogate:
				changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
			case svc.Stop, svc.Shutdown:
				changes <- svc.Status{State: svc.StopPending}
				once.Do(cancel)
				if err := <-finished; err != nil {
					return false, 1
				}
				return false, 0
			}
		case err := <-finished:
			cancel()
			if err != nil {
				return false, 1
			}
			return false, 0
		}
	}
}
