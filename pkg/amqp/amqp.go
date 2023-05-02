package amqp

import (
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"
)

// Defines our interface for connecting and consuming messages.
type IMessagingClient interface {
	ConnectToBroker(connectionString string)
	Publish(msg []byte, exchangeName string, exchangeType string) error
	PublishOnQueue(msg []byte, queueName string) error
	Subscribe(exchangeName string, exchangeType string, consumerName string, handlerFunc func(amqp.Delivery)) error
	SubscribeToQueue(queueName string, consumerName string, handlerFunc func(amqp.Delivery)) error
	Close()
}

type Exchange struct {
	ExchangeName string
	ExchangeType string
	Durable      bool
	AutoDelete   bool
	Internal     bool
	NoWait       bool
	Arguments    []struct {
		ArgumentKey   string
		ArgumentValue string
	}
}

type Queue struct {
	QueueName  string
	Durable    bool
	AutoDelete bool
	Exclusive  bool
	NoWait     bool
	Arguments  []struct {
		ArgumentKey   string
		ArgumentValue string
	}
}

// Real implementation, encapsulates a pointer to an amqp.Connection
type MessagingClient struct {
	conn   *amqp.Connection
	logger *zap.SugaredLogger
}

func NewMessagingClient(connectionString string) *MessagingClient {
	conn, err := amqp.Dial(connectionString)
	if err != nil {
		panic("Failed to connect to AMQP compatible broker at: " + connectionString)
	}

	return &MessagingClient{
		conn: conn,
	}
}

func (m *MessagingClient) Publish(body []byte, exchange *Exchange, queue *Queue) error {
	if m.conn == nil {
		panic("Tried to send message before connection was initialized. Don't do that.")
	}
	ch, err := m.conn.Channel() // Get a channel from the connection
	if err != nil {
		m.logger.Fatal("Cannot open RabbitMQ channel", err)
	}
	defer ch.Close()

	exchangeArgs := amqp.Table{}
	for i := 0; i < len(exchange.Arguments); i++ {
		exchangeArgs[exchange.Arguments[i].ArgumentKey] = exchange.Arguments[i].ArgumentValue
	}

	err = ch.ExchangeDeclare(
		exchange.ExchangeName, // name of the exchange
		exchange.ExchangeType, // type
		exchange.Durable,      // durable
		exchange.AutoDelete,   // delete when complete
		exchange.Internal,     // internal
		exchange.Internal,     // noWait
		exchangeArgs,          // arguments
	)
	failOnError(err, "Failed to register an Exchange")

	queueArgs := amqp.Table{}
	for i := 0; i < len(queue.Arguments); i++ {
		queueArgs[queue.Arguments[i].ArgumentKey] = queue.Arguments[i].ArgumentValue
	}

	_, err = ch.QueueDeclare( // Declare a queue that will be created if not exists with some args
		queue.QueueName,  // our queue name
		queue.Durable,    // durable
		queue.AutoDelete, // delete when unused
		queue.Exclusive,  // exclusive
		queue.NoWait,     // no-wait
		queueArgs,        // arguments
	)
	if err != nil {
		m.logger.Fatal("Failed declaring a queue", err)
	}

	err = ch.QueueBind(
		queue.QueueName,       // name of the queue
		exchange.ExchangeName, // bindingKey
		exchange.ExchangeName, // sourceExchange
		queue.NoWait,          // noWait
		nil,                   // arguments
	)
	if err != nil {
		m.logger.Fatal("Failed binding a queue", err)
	}

	err = ch.Publish( // Publishes a message onto the queue.
		exchange.ExchangeName, // exchange
		exchange.ExchangeName, // routing key      q.Name
		false,                 // mandatory
		false,                 // immediate
		amqp.Publishing{
			Body: body, // Our JSON body as []byte
		})
	// fmt.Printf("A message was sent: %v", body)
	return err
}

func (m *MessagingClient) PublishOnQueue(body []byte, queueName string) error {
	if m.conn == nil {
		panic("Tried to send message before connection was initialized. Don't do that.")
	}
	ch, err := m.conn.Channel() // Get a channel from the connection
	if err != nil {
		m.logger.Fatal("Failed creating a channel", err)
	}
	defer ch.Close()

	queue, err := ch.QueueDeclare( // Declare a queue that will be created if not exists with some args
		queueName, // our queue name
		false,     // durable
		false,     // delete when unused
		false,     // exclusive
		false,     // no-wait
		nil,       // arguments
	)
	if err != nil {
		m.logger.Fatal("Failed declaring a queue", err)
	}

	// Publishes a message onto the queue.
	err = ch.Publish(
		"",         // exchange
		queue.Name, // routing key
		false,      // mandatory
		false,      // immediate
		amqp.Publishing{
			ContentType: "application/json",
			Body:        body, // Our JSON body as []byte
		})
	if err != nil {
		m.logger.Fatal("Failed publishing a message", err)
	}
	// fmt.Printf("A message was sent to queue %v: %v", queueName, body)
	return err
}

func (m *MessagingClient) Subscribe(exchange *Exchange, queue *Queue, consumerName string, handlerFunc func(amqp.Delivery)) error {
	ch, err := m.conn.Channel()
	failOnError(err, "Failed to open a channel")
	// defer ch.Close()

	exchangeArgs := amqp.Table{}
	for i := 0; i < len(exchange.Arguments); i++ {
		exchangeArgs[exchange.Arguments[i].ArgumentKey] = exchange.Arguments[i].ArgumentValue
	}

	err = ch.ExchangeDeclare(
		exchange.ExchangeName, // name of the exchange
		exchange.ExchangeType, // type
		exchange.Durable,      // durable
		exchange.AutoDelete,   // delete when complete
		exchange.Internal,     // internal
		exchange.NoWait,       // noWait
		exchangeArgs,          // arguments
	)
	failOnError(err, "Failed to register an Exchange")

	queueArgs := amqp.Table{}
	for i := 0; i < len(queue.Arguments); i++ {
		queueArgs[queue.Arguments[i].ArgumentKey] = queue.Arguments[i].ArgumentValue
	}

	fmt.Printf("declared Exchange, declaring Queue (%s)", "")
	declaredQueue, err := ch.QueueDeclare(
		queue.QueueName,  // name of the queue
		queue.Durable,    // durable
		queue.AutoDelete, // delete when usused
		queue.Exclusive,  // exclusive
		queue.Exclusive,  // noWait
		queueArgs,        // arguments
	)
	failOnError(err, "Failed to register an Queue")

	fmt.Printf("declared Queue (%d messages, %d consumers), binding to Exchange (key '%s')",
		declaredQueue.Messages, declaredQueue.Consumers, exchange.ExchangeName)

	err = ch.QueueBind(
		queue.QueueName,       // name of the queue
		exchange.ExchangeName, // bindingKey
		exchange.ExchangeName, // sourceExchange
		exchange.NoWait,       // noWait
		nil,                   // arguments
	)
	if err != nil {
		return fmt.Errorf("Queue Bind: %s", err)
	}

	msgs, err := ch.Consume(
		queue.QueueName, // queue
		consumerName,    // consumer
		true,            // auto-ack
		queue.Exclusive, // exclusive
		false,           // no-local
		queue.NoWait,    // no-wait
		nil,             // args
	)
	failOnError(err, "Failed to register a consumer")

	go consumeLoop(msgs, handlerFunc)
	return nil
}

func (m *MessagingClient) SubscribeToQueue(queueName string, consumerName string, handlerFunc func(amqp.Delivery)) error {
	ch, err := m.conn.Channel()
	failOnError(err, "Failed to open a channel")

	fmt.Printf("Declaring Queue (%s)", queueName)
	queue, err := ch.QueueDeclare(
		queueName, // name of the queue
		false,     // durable
		false,     // delete when usused
		false,     // exclusive
		false,     // noWait
		nil,       // arguments
	)
	failOnError(err, "Failed to register an Queue")

	msgs, err := ch.Consume(
		queue.Name,   // queue
		consumerName, // consumer
		true,         // auto-ack
		false,        // exclusive
		false,        // no-local
		false,        // no-wait
		nil,          // args
	)
	failOnError(err, "Failed to register a consumer")

	go consumeLoop(msgs, handlerFunc)
	return nil
}

func (m *MessagingClient) Close() {
	if m.conn != nil {
		m.conn.Close()
	}
}

func consumeLoop(deliveries <-chan amqp.Delivery, handlerFunc func(d amqp.Delivery)) {
	for d := range deliveries {
		// Invoke the handlerFunc func we passed as parameter.
		handlerFunc(d)
	}
}

func failOnError(err error, msg string) {
	if err != nil {
		panic(fmt.Sprintf("%s: %s", msg, err))
	}
}

// func GetAMQPChannel() (*amqp.Channel, error) {
// 	conn, err := amqp.Dial("amqp://localhost")
// 	failOnError(err, "Failed to connect to RabbitMQ")
// 	defer conn.Close()

// 	ch, err := conn.Channel()
// 	failOnError(err, "Failed to open a channel")
// 	defer ch.Close()

// 	return ch, err
// }
