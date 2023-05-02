/*
Copyright © 2023 André Branco Veiga <@__aveiga>
*/
package cmd

import (
	"bytes"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"text/template"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

type ApplicationManifest struct {
	Applications []struct {
		Name      string
		Instances int32
		Memory    string
		Timeout   int32
		Image     string
		Routes    []struct {
			Route string
		}
		Services []struct {
			Service   string
			Instances []struct {
				Instance string
			}
		}
		Env map[string]string
	}
}

var manifestPath string

var targetNamespace string = os.Getenv("ORG") + "-" + os.Getenv("SPACE")

// rabbitmqCmd represents the rabbitmq command
var pushCmd = &cobra.Command{
	Use:   "push",
	Short: "ksm application push",
	Run: func(cmd *cobra.Command, args []string) {
		if os.Getenv("ORG") == "" || os.Getenv("SPACE") == "" {
			panic("ORG and SPACE not defined")
		}

		var kubeconfig *string
		if home := homedir.HomeDir(); home != "" {
			kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
		} else {
			kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
		}
		flag.Parse()

		// use the current context in kubeconfig
		config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
		if err != nil {
			panic(err.Error())
		}

		// create the clientset
		clientset, err := kubernetes.NewForConfig(config)
		if err != nil {
			panic(err.Error())
		}

		namespace, err := clientset.CoreV1().Namespaces().Get(cmd.Context(), targetNamespace, metav1.GetOptions{})
		if err != nil {
			nsSpec := v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: targetNamespace}}
			namespace, err := clientset.CoreV1().Namespaces().Create(cmd.Context(), &nsSpec, metav1.CreateOptions{})
			if err != nil {
				panic(err.Error())
			}
			fmt.Println(namespace.Name + " created")
		}
		fmt.Println("Pushing to namespace %s", namespace.Name)

		var fullManifestPath string
		currentDir, err := os.Getwd()
		if err != nil {
			panic(err.Error())
		}
		fullManifestPath = filepath.Join(currentDir, manifestPath)
		// fmt.Println(fullManifestPath)

		manifestFile, err := ioutil.ReadFile(fullManifestPath)
		if err != nil {
			panic(err.Error())
		}

		var applicationManifest ApplicationManifest
		applicationManifestUnmarshallError := yaml.Unmarshal(manifestFile, &applicationManifest)
		if applicationManifestUnmarshallError != nil {
			panic(err.Error())
		}

		// fmt.Println(applicationManifest.Applications[0].Services[0].Service)

		var appSvcBindingInfo map[string]map[string]map[string]string = make(map[string]map[string]map[string]string)

		// Service Instance Creation
		for _, app := range applicationManifest.Applications {
			appSvcBindingInfo[app.Name] = make(map[string]map[string]string)

			for _, serviceDefinition := range app.Services {
				if serviceDefinition.Service == "postgres" {
					for _, serviceInstanceDefinition := range serviceDefinition.Instances {
						type PostgresServiceInstance struct {
							Name string
						}

						pgServiceInstance := PostgresServiceInstance{serviceInstanceDefinition.Instance}
						appSvcBindingInfo[app.Name][pgServiceInstance.Name] = make(map[string]string)

						tmpl, err := template.ParseFiles("templates/postgres.json")
						if err != nil {
							panic(err.Error())
						}
						var templateInstance bytes.Buffer
						err = tmpl.Execute(&templateInstance, pgServiceInstance)
						if err != nil {
							panic(err.Error())
						}

						response := clientset.RESTClient().Post().AbsPath(fmt.Sprintf("/apis/postgres-operator.crunchydata.com/v1beta1/namespaces/%s/postgresclusters", targetNamespace)).Body(templateInstance.Bytes()).Do(cmd.Context())
						var statusCode int
						response.StatusCode(&statusCode)

						secrets, err := clientset.CoreV1().Secrets("postgres-operator").Get(cmd.Context(), pgServiceInstance.Name+"-pguser-"+pgServiceInstance.Name, metav1.GetOptions{})
						if err != nil {
							panic(err.Error())
						}

						appSvcBindingInfo[app.Name][pgServiceInstance.Name]["dbname"] = string(secrets.Data["dbname"])
						appSvcBindingInfo[app.Name][pgServiceInstance.Name]["host"] = string(secrets.Data["host"])
						appSvcBindingInfo[app.Name][pgServiceInstance.Name]["jdbc-uri"] = string(secrets.Data["jdbc-uri"])
						appSvcBindingInfo[app.Name][pgServiceInstance.Name]["user"] = string(secrets.Data["user"])
						appSvcBindingInfo[app.Name][pgServiceInstance.Name]["password"] = string(secrets.Data["password"])
						appSvcBindingInfo[app.Name][pgServiceInstance.Name]["port"] = string(secrets.Data["port"])
						appSvcBindingInfo[app.Name][pgServiceInstance.Name]["uri"] = string(secrets.Data["uri"])
						appSvcBindingInfo[app.Name][pgServiceInstance.Name]["verifier"] = string(secrets.Data["verifier"])
					}

				}
			}

			// Temporary svc instance creds inject strategy
			// TODO refactor
			for _, svcInstanceDetails := range appSvcBindingInfo[app.Name] {
				for variable, val := range svcInstanceDetails {
					app.Env[variable] = val
				}
			}

			var applicationEnvVars []v1.EnvVar

			for variable, value := range app.Env {
				applicationEnvVars = append(applicationEnvVars, v1.EnvVar{Name: variable, Value: value})
			}

			deploymentsClient := clientset.AppsV1().Deployments(targetNamespace)

			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name: app.Name,
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: &app.Instances,
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"app": app.Name,
						},
					},
					Template: v1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"app": app.Name,
							},
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name:  app.Name,
									Image: app.Image,
									Ports: []v1.ContainerPort{
										{
											Name:          "http",
											Protocol:      v1.ProtocolTCP,
											ContainerPort: 8080,
										},
									},
									Env: applicationEnvVars,
								},
							},
						},
					},
				},
			}

			// Create Deployment
			fmt.Println("Creating deployment...")
			deploymentCreationResult, err := deploymentsClient.Create(cmd.Context(), deployment, metav1.CreateOptions{})
			if err != nil {
				panic(err)
			}
			fmt.Printf("Created deployment %q.\n", deploymentCreationResult.GetObjectMeta().GetName())

		}
		// // fmt.Println("Got the following flags", connection, producers, consumers, payloadSize, queueName)
		// client := amqp.NewMessagingClient(connection)
		// serializedPayload := make([]byte, payloadSize)

		// if declarationJson != "" {
		// 	content, err := ioutil.ReadFile(declarationJson)
		// 	if err != nil {
		// 		log.Fatal("Error when opening file: ", err)
		// 	}

		// 	declaration, err := serialization.Deserialize[DeclarationJson](content)
		// 	if err != nil {
		// 		log.Fatal("Error deserializing file: ", err)
		// 	}

		// 	for i := 1; i <= consumers; i++ {
		// 		wg.Add(1)
		// 		SetupComplexConsumer(client, declaration)
		// 	}

		// 	for i := 1; i <= producers; i++ {
		// 		// fmt.Println("Setting up producer")
		// 		wg.Add(1)
		// 		go SetupComplexProducer(client, serializedPayload, declaration)
		// 	}

		// } else {
		// 	for i := 1; i <= consumers; i++ {
		// 		wg.Add(1)
		// 		SetupSimpleConsumer(client)
		// 	}

		// 	for i := 1; i <= producers; i++ {
		// 		// fmt.Println("Setting up producer")
		// 		wg.Add(1)
		// 		go SetupSimpleProducer(client, serializedPayload)
		// 	}
		// }

		// wg.Wait()
	},
}

// func SetupSimpleProducer(client *amqp.MessagingClient, payload []byte) {
// 	for {
// 		// fmt.Println("publishing")
// 		err := client.PublishOnQueue(payload, queueName)
// 		if err != nil {
// 			fmt.Println(err)
// 		}
// 	}
// }

// func SetupComplexProducer(client *amqp.MessagingClient, payload []byte, declaration DeclarationJson) {
// 	for {
// 		// fmt.Println("publishing")
// 		exchange := amqp.Exchange{
// 			ExchangeName: declaration.ExchangeName,
// 			ExchangeType: declaration.ExchangeType,
// 			Durable:      declaration.Durable,
// 			AutoDelete:   declaration.AutoDelete,
// 			Internal:     declaration.Internal,
// 			NoWait:       declaration.NoWait,
// 			Arguments:    declaration.Arguments,
// 		}
// 		queue := amqp.Queue{
// 			QueueName:  declaration.Queue.QueueName,
// 			Durable:    declaration.Queue.Durable,
// 			AutoDelete: declaration.Queue.AutoDelete,
// 			Exclusive:  declaration.Queue.Exclusive,
// 			NoWait:     declaration.Queue.NoWait,
// 			Arguments:  declaration.Queue.Arguments,
// 		}
// 		err := client.Publish(payload, &exchange, &queue)
// 		if err != nil {
// 			fmt.Println(err)
// 		}
// 	}
// }

// func SetupSimpleConsumer(client *amqp.MessagingClient) {
// 	err := client.SubscribeToQueue(queueName, "Load Tester", func(d amqp091.Delivery) {})
// 	if err != nil {
// 		fmt.Println(err)
// 	}
// }

// func SetupComplexConsumer(client *amqp.MessagingClient, declaration DeclarationJson) {
// 	exchange := amqp.Exchange{
// 		ExchangeName: declaration.ExchangeName,
// 		ExchangeType: declaration.ExchangeType,
// 		Durable:      declaration.Durable,
// 		AutoDelete:   declaration.AutoDelete,
// 		Internal:     declaration.Internal,
// 		NoWait:       declaration.NoWait,
// 		Arguments:    declaration.Arguments,
// 	}
// 	queue := amqp.Queue{
// 		QueueName:  declaration.Queue.QueueName,
// 		Durable:    declaration.Queue.Durable,
// 		AutoDelete: declaration.Queue.AutoDelete,
// 		Exclusive:  declaration.Queue.Exclusive,
// 		NoWait:     declaration.Queue.NoWait,
// 		Arguments:  declaration.Queue.Arguments,
// 	}
// 	err := client.Subscribe(&exchange, &queue, "Load Tester", func(d amqp091.Delivery) {})
// 	if err != nil {
// 		fmt.Println(err)
// 	}
// }

func init() {
	rootCmd.AddCommand(pushCmd)
	pushCmd.Flags().StringVarP(&manifestPath, "manifest", "f", "", "CF Manifest")
}
