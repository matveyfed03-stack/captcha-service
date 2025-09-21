package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"sync"

	captchapb "captcha-service/api/captcha/v1"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	captchaServiceAddr = "localhost:38000" // Убедитесь, что порт совпадает с тем, на котором запускается ваш сервис
	testServerPort     = 8080
)

// parentTemplate - это HTML-страница, которая будет хостить нашу капчу в iframe
const parentTemplate = `
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <title>Captcha Test Page</title>
    <style>
        body { font-family: sans-serif; display: flex; flex-direction: column; align-items: center; padding-top: 50px; }
        iframe { border: 1px solid #ccc; }
    </style>
</head>
<body>
    <h1>Test Harness for Captcha Service</h1>
    <p>The captcha below is loaded from our gRPC service and displayed in an iframe.</p>
    <iframe id="captcha-frame" srcdoc="{{.CaptchaHTML}}"></iframe>
    <h2 id="result"></h2>

    <script>
        const resultEl = document.getElementById('result');
        const challengeId = "{{.ChallengeID}}";

        // Слушаем сообщения из iframe
        window.addEventListener("message", (e) => {
            if (e.data?.type === "captcha:sendData") {
                console.log("Received data from iframe:", e.data.data);
                resultEl.innerText = "Checking solution...";

                // Отправляем решение на наш тестовый сервер, который проксирует его в gRPC
                fetch("/solve", {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({
                        challengeId: challengeId,
                        solution: e.data.data
                    })
                }).then(res => res.json()).then(data => {
                    console.log("Received result from server:", data);
                    if (data.success) {
                        resultEl.innerText = "SUCCESS! Confidence: " + data.confidence + "%";
                        resultEl.style.color = 'green';
                    } else {
                        resultEl.innerText = "FAILED. Please reload and try again.";
                        resultEl.style.color = 'red';
                    }
                });
            }
        });
    </script>
</body>
</html>
`

// gRPCClient управляет соединением и стримом
type gRPCClient struct {
	conn   *grpc.ClientConn
	client captchapb.CaptchaServiceClient
	stream captchapb.CaptchaService_MakeEventStreamClient
	mu     sync.Mutex
}

func (c *gRPCClient) init() error {
	var err error
	// Устанавливаем соединение
	c.conn, err = grpc.Dial(captchaServiceAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("did not connect to captcha service: %w", err)
	}
	c.client = captchapb.NewCaptchaServiceClient(c.conn)

	// Открываем стрим
	c.stream, err = c.client.MakeEventStream(context.Background())
	if err != nil {
		return fmt.Errorf("failed to open event stream: %w", err)
	}

	// В фоне слушаем ответы от сервера (результаты проверки)
	go func() {
		for {
			res, err := c.stream.Recv()
			if err == io.EOF {
				log.Println("gRPC stream closed by server")
				return
			}
			if err != nil {
				log.Printf("Error receiving from gRPC stream: %v", err)
				return
			}
			log.Printf("Received async result from captcha service: ChallengeID=%s, Confidence=%d",
				res.GetResult().GetChallengeId(), res.GetResult().GetConfidencePercent())
		}
	}()

	return nil
}

func main() {
	// Инициализируем нашего gRPC клиента
	client := &gRPCClient{}
	if err := client.init(); err != nil {
		log.Fatalf("Failed to initialize gRPC client: %v", err)
	}
	defer client.conn.Close()

	tmpl := template.Must(template.New("").Parse(parentTemplate))

	// HTTP-хендлер для главной страницы
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// 1. Запрашиваем новую капчу у сервиса
		res, err := client.client.NewChallenge(context.Background(), &captchapb.ChallengeRequest{Complexity: 50})
		if err != nil {
			http.Error(w, "Failed to get challenge from service", http.StatusInternalServerError)
			log.Printf("Error from NewChallenge: %v", err)
			return
		}

		// 2. Рендерим страницу-обертку с iframe
		data := map[string]interface{}{
			"CaptchaHTML": template.HTML(res.Html),
			"ChallengeID": res.ChallengeId,
		}
		w.Header().Set("Content-Type", "text/html")
		tmpl.Execute(w, data)
	})

	// HTTP-хендлер для приема решения от фронтенда
	http.HandleFunc("/solve", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ChallengeID string `json:"challengeId"`
			Solution    string `json:"solution"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// 3. Отправляем решение в gRPC-стрим
		client.mu.Lock()
		defer client.mu.Unlock()
		err := client.stream.Send(&captchapb.ClientEvent{
			EventType:   captchapb.ClientEvent_FRONTEND_EVENT,
			ChallengeId: req.ChallengeID,
			Data:        []byte(req.Solution),
		})
		if err != nil {
			http.Error(w, "Failed to send solution via gRPC", http.StatusInternalServerError)
			log.Printf("Error sending to gRPC stream: %v", err)
			return
		}

		// В реальной системе ответ придет асинхронно. Здесь для простоты мы просто говорим "ок"
		// А результат смотрим в логах
		w.Header().Set("Content-Type", "application/json")
		// Это заглушка, т.к. реальный ответ приходит в горутине-слушателе
		// Для теста этого достаточно.
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "confidence": "check_logs"})
	})

	log.Printf("Test client web server starting on http://localhost:%d", testServerPort)
	if err := http.ListenAndServe(fmt.Sprintf(":%d", testServerPort), nil); err != nil {
		log.Fatalf("Failed to start test server: %v", err)
	}
}
