package main

import (
	"fmt"
	"io"
	"log"
	"net"

	pb "captcha-service/api/balancer/v1" // Путь к сгенерированному коду

	"google.golang.org/grpc"
)

const mockBalancerPort = 50051

// balancerService - наша реализация-заглушка для сервера балансера
type balancerService struct {
	pb.UnimplementedBalancerServiceServer
}

// RegisterInstance - реализует стриминговый RPC для регистрации инстансов
func (s *balancerService) RegisterInstance(stream pb.BalancerService_RegisterInstanceServer) error {
	log.Println("New captcha instance trying to register...")
	for {
		req, err := stream.Recv()
		if err == io.EOF {
			log.Println("Captcha instance disconnected.")
			return nil
		}
		if err != nil {
			log.Printf("Error receiving from stream: %v", err)
			return err
		}

		// Просто логируем все, что получаем от сервиса капчи
		log.Printf(
			"Received event from captcha instance: ID=%s, Type=%s, Host=%s, Port=%d",
			req.InstanceId,
			req.EventType,
			req.Host,
			req.PortNumber,
		)
	}
}

func main() {
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", mockBalancerPort))
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

	s := grpc.NewServer()
	pb.RegisterBalancerServiceServer(s, &balancerService{})

	log.Printf("Mock balancer server listening at %v", lis.Addr())
	if err := s.Serve(lis); err != nil {
		log.Fatalf("Failed to serve: %v", err)
	}
}
