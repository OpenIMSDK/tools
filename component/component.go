// Copyright © 2023 OpenIM. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package component

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/IBM/sarama"
	"github.com/OpenIMSDK/tools/config"
	"github.com/OpenIMSDK/tools/errs"
	"github.com/go-zookeeper/zk"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"net"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	// defaultCfgPath is the default path of the configuration file.
	defaultCfgPath           = "../../../../../config/config.yaml"
	minioHealthCheckDuration = 1
	maxRetry                 = 300
	componentStartErrCode    = 6000
	configErrCode            = 6001
	mongoConnTimeout         = 30 * time.Second
)

const (
	colorRed    = 31
	colorGreen  = 32
	colorYellow = 33
)

var (
	cfgPath           = flag.String("c", defaultCfgPath, "Path to the configuration file")
	ErrComponentStart = errs.NewCodeError(componentStartErrCode, "ComponentStartErr")
	ErrConfig         = errs.NewCodeError(configErrCode, "Config file is incorrect")
)

// Helper function to get environment variable or default value
func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

// checkMongo checks the MongoDB connection without retries
func CheckMongo(mongoStu config.Mongo) (string, error) {
	uri := getEnv("MONGO_URI", buildMongoURI(mongoStu))

	ctx, cancel := context.WithTimeout(context.Background(), mongoConnTimeout)
	defer cancel()

	str := "ths addr is:" + strings.Join(mongoStu.Address, ",")

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		return "", errs.Wrap(ErrStr(err, str))
	}
	defer client.Disconnect(context.Background())

	ctx, cancel = context.WithTimeout(context.Background(), mongoConnTimeout)
	defer cancel()

	if err = client.Ping(ctx, nil); err != nil {
		return "", errs.Wrap(ErrStr(err, str))
	}

	return str, nil
}

// buildMongoURI constructs the MongoDB URI using configuration settings
func buildMongoURI(mongoStu config.Mongo) string {
	// Fallback to config if environment variables are not set
	username := mongoStu.Username
	password := mongoStu.Password
	database := mongoStu.Database
	maxPoolSize := mongoStu.MaxPoolSize

	mongodbHosts := strings.Join(mongoStu.Address, ",")

	if username != "" && password != "" {
		return fmt.Sprintf("mongodb://%s:%s@%s/%s?maxPoolSize=%d",
			username, password, mongodbHosts, database, maxPoolSize)
	}
	return fmt.Sprintf("mongodb://%s/%s?maxPoolSize=%d",
		mongodbHosts, database, maxPoolSize)
}

func exactIP(urll string) string {
	u, _ := url.Parse(urll)
	host, _, err := net.SplitHostPort(u.Host)
	if err != nil {
		host = u.Host
	}
	if strings.HasSuffix(host, ":") {
		host = host[0 : len(host)-1]
	}

	return host
}

// checkMinio checks the MinIO connection
func CheckMinio(minioStu config.Object) (string, error) {
	// Check if MinIO is enabled
	if minioStu.Enable != "minio" {
		return "", nil
	}

	// Prioritize environment variables
	endpoint := getEnv("MINIO_ENDPOINT", minioStu.Minio.Endpoint)
	accessKeyID := getEnv("MINIO_ACCESS_KEY_ID", minioStu.Minio.AccessKeyID)
	secretAccessKey := getEnv("MINIO_SECRET_ACCESS_KEY", minioStu.Minio.SecretAccessKey)
	useSSL := getEnv("MINIO_USE_SSL", "false") // Assuming SSL is not used by default

	if endpoint == "" || accessKeyID == "" || secretAccessKey == "" {
		return "", ErrConfig.Wrap("MinIO configuration missing")
	}

	// Parse endpoint URL to determine if SSL is enabled
	u, err := url.Parse(endpoint)
	if err != nil {
		str := "the endpoint is:" + endpoint
		return "", errs.Wrap(ErrStr(err, str))
	}
	secure := u.Scheme == "https" || useSSL == "true"

	// Initialize MinIO client
	minioClient, err := minio.New(u.Host, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKeyID, secretAccessKey, ""),
		Secure: secure,
	})
	str := "ths addr is:" + u.Host
	if err != nil {
		strs := fmt.Sprintf("%v;host:%s,accessKeyID:%s,secretAccessKey:%s,Secure:%v", err, u.Host, accessKeyID, secretAccessKey, secure)
		return "", errs.Wrap(err, strs)
	}

	// Perform health check
	cancel, err := minioClient.HealthCheck(time.Duration(minioHealthCheckDuration) * time.Second)
	if err != nil {
		return "", errs.Wrap(ErrStr(err, str))
	}
	defer cancel()

	if minioClient.IsOffline() {
		str := fmt.Sprintf("Minio server is offline;%s", str)
		return "", ErrComponentStart.Wrap(str)
	}

	// Check for localhost in API URL and Minio SignEndpoint
	if exactIP(minioStu.ApiURL) == "127.0.0.1" || exactIP(minioStu.Minio.SignEndpoint) == "127.0.0.1" {
		return "", ErrConfig.Wrap("apiURL or Minio SignEndpoint endpoint contain 127.0.0.1")
	}

	return str, nil
}

// checkRedis checks the Redis connection
func CheckRedis(redisStu config.Redis) (string, error) {
	// Prioritize environment variables
	address := getEnv("REDIS_ADDRESS", strings.Join(redisStu.Address, ","))
	username := getEnv("REDIS_USERNAME", redisStu.Username)
	password := getEnv("REDIS_PASSWORD", redisStu.Password)

	// Split address to handle multiple addresses for cluster setup
	redisAddresses := strings.Split(address, ",")

	var redisClient redis.UniversalClient
	if len(redisAddresses) > 1 {
		// Use cluster client for multiple addresses
		redisClient = redis.NewClusterClient(&redis.ClusterOptions{
			Addrs:    redisAddresses,
			Username: username,
			Password: password,
		})
	} else {
		// Use regular client for single address
		redisClient = redis.NewClient(&redis.Options{
			Addr:     redisAddresses[0],
			Username: username,
			Password: password,
		})
	}
	defer redisClient.Close()

	// Ping Redis to check connectivity
	_, err := redisClient.Ping(context.Background()).Result()
	str := "the addr is:" + strings.Join(redisAddresses, ",")
	if err != nil {
		return "", errs.Wrap(ErrStr(err, str))
	}

	return str, nil
}

// checkZookeeper checks the Zookeeper connection
func CheckZookeeper(zkStu config.Zookeeper) (string, error) {
	// Prioritize environment variables
	schema := getEnv("ZOOKEEPER_SCHEMA", "digest")
	address := getEnv("ZOOKEEPER_ADDRESS", strings.Join(zkStu.ZkAddr, ","))
	username := getEnv("ZOOKEEPER_USERNAME", zkStu.Username)
	password := getEnv("ZOOKEEPER_PASSWORD", zkStu.Password)

	// Split addresses to handle multiple Zookeeper nodes
	zookeeperAddresses := strings.Split(address, ",")

	// Connect to Zookeeper
	str := "the addr is:" + address
	c, eventChan, err := zk.Connect(zookeeperAddresses, time.Second) // Adjust the timeout as necessary
	if err != nil {
		return "", errs.Wrap(ErrStr(err, str))
	}
	timeout := time.After(5 * time.Second)
	for {
		select {
		case event := <-eventChan:
			if event.State == zk.StateConnected {
				fmt.Println("Connected to Zookeeper")
				goto Connected
			}
		case <-timeout:
			return "", errs.Wrap(errors.New("timeout waiting for Zookeeper connection"), "Zookeeper Addr: "+strings.Join(zkStu.ZkAddr, " "))
		}
	}
Connected:
	defer c.Close()

	// Set authentication if username and password are provided
	if username != "" && password != "" {
		if err := c.AddAuth(schema, []byte(username+":"+password)); err != nil {
			return "", errs.Wrap(ErrStr(err, str))
		}
	}

	return str, nil
}

// checkKafka checks the Kafka connection
func CheckKafka(kafkaStu config.Kafka) (string, error) {
	// Prioritize environment variables
	username := getEnv("KAFKA_USERNAME", kafkaStu.Username)
	password := getEnv("KAFKA_PASSWORD", kafkaStu.Password)
	address := getEnv("KAFKA_ADDRESS", strings.Join(kafkaStu.Addr, ","))

	// Split addresses to handle multiple Kafka brokers
	kafkaAddresses := strings.Split(address, ",")

	// Configure Kafka client
	cfg := sarama.NewConfig()
	if username != "" && password != "" {
		cfg.Net.SASL.Enable = true
		cfg.Net.SASL.User = username
		cfg.Net.SASL.Password = password
	}
	// Additional Kafka setup (e.g., TLS configuration) can be added here
	// kafka.SetupTLSConfig(cfg)

	// Create Kafka client
	str := "the addr is:" + address
	kafkaClient, err := sarama.NewClient(kafkaAddresses, cfg)
	if err != nil {
		return "", errs.Wrap(ErrStr(err, str))
	}
	defer kafkaClient.Close()

	// Verify if necessary topics exist
	topics, err := kafkaClient.Topics()
	if err != nil {
		return "", errs.Wrap(err)
	}

	requiredTopics := []string{
		kafkaStu.MsgToMongo.Topic,
		kafkaStu.MsgToPush.Topic,
		kafkaStu.LatestMsgToRedis.Topic,
	}

	for _, requiredTopic := range requiredTopics {
		if !IsTopicPresent(requiredTopic, topics) {
			return "", ErrComponentStart.Wrap(fmt.Sprintf("Kafka doesn't contain topic: %v", requiredTopic))
		}
	}

	return str, nil
}

// isTopicPresent checks if a topic is present in the list of topics
func IsTopicPresent(topic string, topics []string) bool {
	for _, t := range topics {
		if t == topic {
			return true
		}
	}
	return false
}

func colorPrint(colorCode int, format string, a ...interface{}) {
	fmt.Printf("\x1b[%dm%s\x1b[0m\n", colorCode, fmt.Sprintf(format, a...))
}

func ErrorPrint(s string) {
	colorPrint(colorRed, "%v", s)
}

func SuccessPrint(s string) {
	colorPrint(colorGreen, "%v", s)
}

func WarningPrint(s string) {
	colorPrint(colorYellow, "Warning: But %v", s)
}

func ErrStr(err error, str string) error {
	return fmt.Errorf("%v;%s", err, str)
}
