package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"time"

	// Импортируем сгенерированные пакеты
	balancerpb "captcha-service/api/balancer/v1"
	captchapb "captcha-service/api/captcha/v1"

	"github.com/google/uuid"
	"github.com/patrickmn/go-cache"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	minPort           = 38000
	maxPort           = 40000
	balancerAddr      = "localhost:50051" // Адрес нашего mock-балансера
	challengeType     = "simple-button"
	instanceHost      = "localhost" // Хост, на котором запущен наш сервис
	heartbeatInterval = 15 * time.Second
	defaultExpiration = 5 * time.Minute
	cleanupInterval   = 10 * time.Minute
)

// captchaService реализует интерфейс CaptchaServiceServer
type captchaService struct {
	captchapb.UnimplementedCaptchaServiceServer
	challenges *cache.Cache
}

// NewChallenge - создает новое задание капчи
func (s *captchaService) NewChallenge(ctx context.Context, req *captchapb.ChallengeRequest) (*captchapb.ChallengeResponse, error) {
	challengeID := uuid.New().String()
	log.Printf("Generating new challenge with ID: %s", challengeID)

	htmlContent, err := os.ReadFile("static/captcha.html")
	if err != nil {
		log.Printf("Error reading static html: %v", err)
		return nil, fmt.Errorf("internal server error")
	}

	// ИЗМЕНЕНИЕ: Сохраняем ответ в кэш с TTL по умолчанию (5 минут)
	s.challenges.Set(challengeID, "success", cache.DefaultExpiration)

	return &captchapb.ChallengeResponse{
		ChallengeId: challengeID,
		Html:        string(htmlContent),
	}, nil
}

// MakeEventStream - пока просто "пустышка" для стрима событий
func (s *captchaService) MakeEventStream(stream captchapb.CaptchaService_MakeEventStreamServer) error {
	log.Println("Client connected to event stream.")

	for {
		event, err := stream.Recv()
		// ... (обработка ошибок стрима без изменений) ...
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
			clientSolution := string(event.GetData())

			log.Printf("Received solution for challenge %s: '%s'", challengeID, clientSolution)

			// ИЗМЕНЕНИЕ: Получаем ответ из кэша
			expected, found := s.challenges.Get(challengeID)
			if !found {
				log.Printf("Challenge ID %s not found (expired or already solved).", challengeID)
				continue // Игнорируем
			}
			expectedSolution := expected.(string) // Приводим тип

			var confidence int32 = 0
			if clientSolution == expectedSolution {
				confidence = 100
				log.Printf("Challenge %s solved SUCCESSFULLY.", challengeID)
			} else {
				log.Printf("Challenge %s FAILED. Expected '%s', got '%s'.", challengeID, expectedSolution, clientSolution)
			}

			// ... (отправка результата без изменений) ...
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

			// ИЗМЕНЕНИЕ: Удаляем решенный челлендж из кэша, чтобы не ждать TTL
			s.challenges.Delete(challengeID)
		}
	}
}

func main() {
	// 1. Находим свободный порт
	port, err := findFreePort(minPort, maxPort)
	if err != nil {
		log.Fatalf("Failed to find a free port: %v", err)
	}
	log.Printf("Found free port: %d", port)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		log.Fatalf("Failed to listen on port %d: %v", port, err)
	}

	// 2. Запускаем gRPC сервер капчи
	grpcServer := grpc.NewServer()
	c := cache.New(defaultExpiration, cleanupInterval)
	service := &captchaService{
		challenges: c,
	}
	captchapb.RegisterCaptchaServiceServer(grpcServer, service)

	log.Printf("Captcha gRPC server listening at %v", lis.Addr())

	// 3. В отдельной горутине подключаемся к балансеру
	go connectToBalancer(instanceHost, port)

	// 4. Запускаем сервер
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("Failed to serve gRPC: %v", err)
	}
}

// findFreePort ищет первый свободный TCP порт в заданном диапазоне
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

// connectToBalancer подключается к балансеру и регистрирует наш инстанс
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

	// Отправляем первое сообщение о готовности
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

	// Запускаем "пульс" (heartbeat), чтобы периодически сообщать о готовности
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for range ticker.C {
		req.Timestamp = time.Now().Unix()
		if err := stream.Send(req); err != nil {
			log.Printf("Failed to send heartbeat: %v", err)
			// В реальном приложении здесь была бы логика переподключения
			return
		}
	}
}
