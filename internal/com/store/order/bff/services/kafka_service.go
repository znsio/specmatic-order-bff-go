package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/segmentio/kafka-go"
	"github.com/znsio/specmatic-order-bff-go/internal/com/store/order/bff/config"
	"github.com/znsio/specmatic-order-bff-go/internal/com/store/order/bff/models"
)

func SendProductMessages(products []models.Product) error {
	cfg, err := config.LoadConfig()
	if err != nil {
		return fmt.Errorf("error loading config: %w", err)
	}

	// Create a new Kafka writer with more configuration options
	w := kafka.NewWriter(kafka.WriterConfig{
		Brokers:      []string{cfg.KafkaHost + ":" + cfg.KafkaPort},
		Topic:        cfg.KafkaTopic,
		Balancer:     &kafka.LeastBytes{},
		WriteTimeout: 10 * time.Second,
		ReadTimeout:  10 * time.Second,
		Async:        false, // Set to true for better performance, but less reliability
	})
	defer w.Close()

	if len(products) > 0 {
		if err := sendSingleProduct(w, products[0]); err != nil {
			log.Printf("Error sending product (ID: %d): %v", products[0].ID, err)
			return err
		}
	}

	return nil
}

func sendSingleProduct(w *kafka.Writer, product models.Product) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	productMessage := models.ProductMessage{
		ID:        product.ID,
		Name:      product.Name,
		Inventory: product.Inventory,
	}

	messageValue, err := json.Marshal(productMessage)
	if err != nil {
		return fmt.Errorf("error marshaling product message: %w", err)
	}

	err = w.WriteMessages(ctx, kafka.Message{
		Key:   []byte(strconv.Itoa(product.ID)),
		Value: messageValue,
	})
	if err != nil {
		return fmt.Errorf("error writing message to Kafka: %w", err)
	}

	log.Printf("Successfully sent product message for ID: %d", product.ID)
	return nil
}
