package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"time"

	balancerpb "captcha-service/api/balancer/v1"
	captchapb "captcha-service/api/captcha/v1"
	"captcha-service/internal/generator" // <-- Убедитесь, что этот импорт есть

	"github.com/google/uuid"
	"github.com/patrickmn/go-cache"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	defaultExpiration = 5 * time.Minute
	cleanupInterval   = 10 * time.Minute
	minPort           = 38000
	maxPort           = 40000
	balancerAddr      = "localhost:50051"
	challengeType     = "slider-puzzle" // <-- Тип нашей новой капчи
	instanceHost      = "localhost"
	heartbeatInterval = 15 * time.Second
)

// Структура для хранения ответа
type solution struct {
	X          int
	Complexity int
}

// captchaService теперь хранит генератор
type captchaService struct {
	captchapb.UnimplementedCaptchaServiceServer
	challenges *cache.Cache
	generator  *generator.Generator // <-- Поле для генератора
}

// NewChallenge использует генератор
func (s *captchaService) NewChallenge(ctx context.Context, req *captchapb.ChallengeRequest) (*captchapb.ChallengeResponse, error) {
	challengeID := uuid.New().String()
	log.Printf("Generating new slider-puzzle challenge (complexity %d) with ID: %s", req.Complexity, challengeID)

	// Вызываем наш генератор
	html, correctX, err := s.generator.Generate()
	if err != nil {
		log.Printf("Failed to generate challenge: %v", err)
		return nil, fmt.Errorf("internal server error")
	}

	// Сохраняем правильный ответ в кэш
	sol := solution{
		X:          correctX,
		Complexity: int(req.GetComplexity()),
	}
	s.challenges.Set(challengeID, sol, cache.DefaultExpiration)

	return &captchapb.ChallengeResponse{
		ChallengeId: challengeID,
		Html:        html,
	}, nil
}

// MakeEventStream проверяет решение для пазла
func (s *captchaService) MakeEventStream(stream captchapb.CaptchaService_MakeEventStreamServer) error {
	log.Println("Client connected to event stream.")
	for {
		event, err := stream.Recv()
		if err == io.EOF {
			log.Println("Client stream closed.")
			return nil
		}
		if err != nil {
			log.Printf("Error receiving event: %v", err)
			return err
		}

		if event.EventType == captchapb.ClientEvent_FRONTEND_EVENT {
			challengeID := event.GetChallengeId()
			clientXStr := string(event.GetData())
			clientX, err := strconv.Atoi(clientXStr)
			if err != nil {
				log.Printf("Failed to parse client solution for %s: %v", challengeID, err)
				continue
			}

			log.Printf("Received solution for challenge %s: X=%d", challengeID, clientX)

			expected, found := s.challenges.Get(challengeID)
			if !found {
				log.Printf("Challenge ID %s not found (expired or already solved).", challengeID)
				continue
			}
			sol := expected.(solution)

			tolerance := 5 - (sol.Complexity / 25)
			if tolerance < 1 {
				tolerance = 1
			}

			var confidence int32 = 0
			delta := clientX - sol.X
			if delta < 0 {
				delta = -delta
			}

			if delta <= tolerance {
				confidence = 100
				log.Printf("Challenge %s solved SUCCESSFULLY (delta: %d, tolerance: %d).", challengeID, delta, tolerance)
			} else {
				log.Printf("Challenge %s FAILED. Expected ~%d, got %d (delta: %d, tolerance: %d).", challengeID, sol.X, clientX, delta, tolerance)
			}

			resultEvent := &captchapb.ServerEvent{
				Event: &captchapb.ServerEvent_Result{
					Result: &captchapb.ServerEvent_ChallengeResult{
						ChallengeId:       challengeID,
						ConfidencePercent: confidence,
					},
				},
			}
			if err := stream.Send(resultEvent); err != nil {
				log.Printf("Failed to send result for challenge %s: %v", challengeID, err)
			}
			s.challenges.Delete(challengeID)
		}
	}
}

// main инициализирует сервис с генератором
func main() {
	port, err := findFreePort(minPort, maxPort)
	if err != nil {
		log.Fatalf("Failed to find a free port: %v", err)
	}
	log.Printf("Found free port: %d", port)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		log.Fatalf("Failed to listen on port %d: %v", port, err)
	}

	grpcServer := grpc.NewServer()

	// Инициализируем генератор
	gen, err := generator.New()
	if err != nil {
		log.Fatalf("Failed to create captcha generator: %v", err)
	}

	c := cache.New(defaultExpiration, cleanupInterval)
	// Создаем сервис, передавая ему генератор
	service := &captchaService{
		challenges: c,
		generator:  gen,
	}
	captchapb.RegisterCaptchaServiceServer(grpcServer, service)

	log.Printf("Captcha gRPC server listening at %v", lis.Addr())

	go connectToBalancer(instanceHost, port)

	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("Failed to serve gRPC: %v", err)
	}
}

func findFreePort(min, max int) (int, error) {
	for port := min; port <= max; port++ {
		addr := fmt.Sprintf(":%d", port)
		l, err := net.Listen("tcp", addr)
		if err == nil {
			l.Close()
			return port, nil
		}
	}
	return 0, fmt.Errorf("no free ports in range %d-%d", min, max)
}

func connectToBalancer(host string, port int) {
	conn, err := grpc.Dial(balancerAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("Did not connect to balancer: %v", err)
	}
	defer conn.Close()

	client := balancerpb.NewBalancerServiceClient(conn)
	stream, err := client.RegisterInstance(context.Background())
	if err != nil {
		log.Fatalf("Failed to open stream to balancer: %v", err)
	}

	instanceID := uuid.New().String()
	log.Printf("Registering instance with ID: %s", instanceID)

	req := &balancerpb.RegisterInstanceRequest{
		EventType:     balancerpb.RegisterInstanceRequest_READY,
		InstanceId:    instanceID,
		ChallengeType: challengeType,
		Host:          host,
		PortNumber:    int32(port),
		Timestamp:     time.Now().Unix(),
	}
	if err := stream.Send(req); err != nil {
		log.Fatalf("Failed to send registration message: %v", err)
	}

	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for range ticker.C {
		req.Timestamp = time.Now().Unix()
		if err := stream.Send(req); err != nil {
			log.Printf("Failed to send heartbeat: %v", err)
			return
		}
	}
}
