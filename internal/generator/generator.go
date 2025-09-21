package generator

import (
	"bytes"
	"embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"log"
	"math/rand"
	"time"
)

//go:embed template.html
var captchaTemplateFS embed.FS

//go:embed assets/background.png
var backgroundAsset []byte

const (
	puzzleWidth  = 60
	puzzleHeight = 60
)

// ChallengeData содержит все данные, необходимые для рендеринга HTML-шаблона
type ChallengeData struct {
	BackgroundImg   string
	PuzzleImg       string
	PuzzleYPos      int
	PuzzleWidth     int
	PuzzleHeight    int
	ContainerWidth  int
	ContainerHeight int
	SliderMax       int
}

// Generator отвечает за создание заданий капчи
type Generator struct {
	bgImage  image.Image
	bgWidth  int
	bgHeight int
	template *template.Template
}

// New создает новый экземпляр генератора
func New() (*Generator, error) {
	rand.Seed(time.Now().UnixNano())

	// Декодируем фоновое изображение из встроенных ассетов
	bg, err := png.Decode(bytes.NewReader(backgroundAsset))
	if err != nil {
		return nil, fmt.Errorf("failed to decode background image: %w", err)
	}
	bounds := bg.Bounds()

	// Парсим HTML-шаблон
	tmpl, err := template.ParseFS(captchaTemplateFS, "template.html")
	if err != nil {
		return nil, fmt.Errorf("failed to parse html template: %w", err)
	}

	return &Generator{
		bgImage:  bg,
		bgWidth:  bounds.Dx(),
		bgHeight: bounds.Dy(),
		template: tmpl,
	}, nil
}

// Generate создает новое задание и возвращает HTML и правильный ответ (координату X)
func (g *Generator) Generate() (string, int, error) {
	// Выбираем случайную позицию для пазла
	// (с отступами, чтобы он не появлялся у самого края)
	maxX := g.bgWidth - puzzleWidth - 10
	maxY := g.bgHeight - puzzleHeight - 10
	puzzleX := rand.Intn(maxX-puzzleWidth) + puzzleWidth // Не слишком близко к левому краю
	puzzleY := rand.Intn(maxY-10) + 10

	// Создаем прямоугольник для вырезания пазла
	puzzleRect := image.Rect(puzzleX, puzzleY, puzzleX+puzzleWidth, puzzleY+puzzleHeight)

	// 1. Создаем изображение пазла
	puzzleImg := image.NewRGBA(image.Rect(0, 0, puzzleWidth, puzzleHeight))
	draw.Draw(puzzleImg, puzzleImg.Bounds(), g.bgImage, image.Pt(puzzleX, puzzleY), draw.Src)

	// 2. Создаем фоновое изображение с "дыркой"
	// Мы просто копируем весь фон, а область пазла оставляем прозрачной (она по умолчанию такая)
	backgroundWithHole := image.NewRGBA(g.bgImage.Bounds())
	draw.Draw(backgroundWithHole, backgroundWithHole.Bounds(), g.bgImage, image.Point{}, draw.Src)
	// Закрашиваем область пазла полупрозрачным черным цветом для визуального эффекта
	holeColor := image.NewUniform(color.RGBA{0, 0, 0, 128})
	draw.Draw(backgroundWithHole, puzzleRect, holeColor, image.Point{}, draw.Src)

	// 3. Кодируем оба изображения в base64
	puzzleBase64, err := imageToBase64(puzzleImg)
	if err != nil {
		return "", 0, err
	}
	backgroundBase64, err := imageToBase64(backgroundWithHole)
	if err != nil {
		return "", 0, err
	}

	// 4. Заполняем шаблон и генерируем HTML
	data := ChallengeData{
		BackgroundImg:   backgroundBase64,
		PuzzleImg:       puzzleBase64,
		PuzzleYPos:      puzzleY,
		PuzzleWidth:     puzzleWidth,
		PuzzleHeight:    puzzleHeight,
		ContainerWidth:  g.bgWidth,
		ContainerHeight: g.bgHeight,
		SliderMax:       g.bgWidth - puzzleWidth, // Максимальное значение слайдера
	}

	var htmlBuffer bytes.Buffer
	if err := g.template.Execute(&htmlBuffer, data); err != nil {
		return "", 0, fmt.Errorf("failed to execute template: %w", err)
	}

	log.Printf("Generated puzzle. Correct X is %d", puzzleX)
	return htmlBuffer.String(), puzzleX, nil
}

// imageToBase64 кодирует image.Image в строку base64
func imageToBase64(img image.Image) (string, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", fmt.Errorf("failed to encode image to png: %w", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}
