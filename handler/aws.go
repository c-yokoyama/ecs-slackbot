package main

import (
	"encoding/base64"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/ecs"
	"github.com/aws/aws-sdk-go/service/kms"
	"log"
	"sort"
	"strconv"
	"strings"
)

// TaskRevTag is set of ecs task revision num and its image tag
type TaskRevTag struct {
	revNum   string
	imageTag string
}

// InstanceInfo is set of EC2 name and id
type InstanceInfo struct {
	name string
	id   string
}

var sess = session.Must(session.NewSessionWithOptions(session.Options{
	Config: aws.Config{Region: aws.String(env.Region)},
}))
var svc = ecs.New(sess)

// ListEcsCluster returns ecs cluster list
func ListEcsCluster() ([]string, error) {
	input := &ecs.ListClustersInput{}

	result, err := svc.ListClusters(input)
	if err != nil {
		handleEcsError(err)
		return nil, err
	}

	var clusters []string
	for _, c := range result.ClusterArns {
		clusters = append(clusters, strings.Split(*c, "/")[1])
	}
	return clusters, nil
}

// ListEcsService returns service names in specified cluster
func ListEcsService(cluster string) ([]string, error) {
	input := &ecs.ListServicesInput{
		Cluster: aws.String(cluster),
	}

	result, err := svc.ListServices(input)
	if err != nil {
		handleEcsError(err)
		return nil, err
	}

	var services []string
	for _, c := range result.ServiceArns {
		services = append(services, strings.Split(*c, "/")[1])
	}
	return services, nil
}

// ListTaskRevsAndImageTags returns ecr tags (expected to be commit hash) with specified cluster and service name
func ListTaskRevsAndImageTags(taskDefName string) ([]TaskRevTag, error) {
	input := &ecs.ListTaskDefinitionsInput{
		FamilyPrefix: aws.String(taskDefName),
	}

	result, err := svc.ListTaskDefinitions(input)
	if err != nil {
		handleEcsError(err)
		return nil, err
	}

	var taskDefs []string
	for _, t := range result.TaskDefinitionArns {
		taskDefs = append(taskDefs, strings.Split(*t, "/")[1])
	}

	// get each task revisions and image tag(=commit hash)
	var taskRevTags []TaskRevTag
	for _, t := range taskDefs {
		input := &ecs.DescribeTaskDefinitionInput{
			TaskDefinition: aws.String(t),
		}
		result, err := svc.DescribeTaskDefinition(input)
		if err != nil {
			handleEcsError(err)
			return nil, err
		}

		tag := strings.Split(*result.TaskDefinition.ContainerDefinitions[0].Image, ":")[1]
		/*
			if tag == "latest" {
				continue
			}
		*/
		taskRevTags = append(taskRevTags, TaskRevTag{
			revNum:   strings.Split(t, ":")[1],
			imageTag: tag,
		})
	}

	sort.Slice(taskRevTags, func(i, j int) bool {
		iNum, _ := strconv.Atoi(taskRevTags[i].revNum)
		jNum, _ := strconv.Atoi(taskRevTags[j].revNum)
		return iNum > jNum
	})

	return taskRevTags, nil
}

// UpdateEcsService updates specified ecs service
func UpdateEcsService(cluster string, service string, taskDefRev string) error {
	input := &ecs.UpdateServiceInput{
		Cluster:        aws.String(cluster),
		Service:        aws.String(service),
		TaskDefinition: aws.String(taskDefRev),
	}

	result, err := svc.UpdateService(input)
	if err != nil {
		handleEcsError(err)
		return err
	}
	log.Printf("[INFO] ecs update resuilt: %+v", result)

	return nil
}

func handleEcsError(err error) {
	if aerr, ok := err.(awserr.Error); ok {
		switch aerr.Code() {
		case ecs.ErrCodeServerException:
			log.Println(ecs.ErrCodeServerException, aerr.Error())
		case ecs.ErrCodeClientException:
			log.Println(ecs.ErrCodeClientException, aerr.Error())
		case ecs.ErrCodeInvalidParameterException:
			log.Println(ecs.ErrCodeInvalidParameterException, aerr.Error())
		case ecs.ErrCodeClusterNotFoundException:
			log.Println(ecs.ErrCodeClusterNotFoundException, aerr.Error())
		case ecs.ErrCodeServiceNotFoundException:
			log.Println(ecs.ErrCodeServiceNotFoundException, aerr.Error())
		case ecs.ErrCodeServiceNotActiveException:
			log.Println(ecs.ErrCodeServiceNotActiveException, aerr.Error())
		case ecs.ErrCodePlatformUnknownException:
			log.Println(ecs.ErrCodePlatformUnknownException, aerr.Error())
		case ecs.ErrCodePlatformTaskDefinitionIncompatibilityException:
			log.Println(ecs.ErrCodePlatformTaskDefinitionIncompatibilityException, aerr.Error())
		case ecs.ErrCodeAccessDeniedException:
			log.Println(ecs.ErrCodeAccessDeniedException, aerr.Error())
		default:
			log.Println(aerr.Error())
		}
	} else {
		log.Println(err.Error())
	}
}

// DecodeString decodes eccrypted str with KMS
func DecodeString(encrypted string) (string, error) {
	svc := kms.New(session.New(), aws.NewConfig().WithRegion(env.Region))
	data, _ := base64.StdEncoding.DecodeString(encrypted)

	input := &kms.DecryptInput{
		CiphertextBlob: []byte(data),
	}
	result, err := svc.Decrypt(input)
	if err != nil {
		return "", err
	}

	res, _ := base64.StdEncoding.DecodeString(base64.StdEncoding.EncodeToString(result.Plaintext))
	return string(res), nil
}

// FilterInstances returns instanceIds matched with prefix
func FilterInstances(prefix string) ([]InstanceInfo, error) {
	svc := ec2.New(sess)

	input := &ec2.DescribeInstancesInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("instance-state-name"),
				Values: []*string{aws.String("running")},
			},
		},
	}

	result, err := svc.DescribeInstances(input)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			default:
				log.Println(aerr.Error())
			}
		} else {
			log.Println(err.Error())
		}
		return nil, err
	}

	var instances []InstanceInfo

	// ToDO: fix loop
	for _, r := range result.Reservations {
		for _, i := range r.Instances {
			for _, j := range i.Tags {
				if *j.Key == "Name" {
					if strings.HasPrefix(*j.Value, prefix) {
						instances = append(instances, InstanceInfo{
							name: *j.Value,
							id:   *i.InstanceId,
						})
					}
					continue
				}
			}
		}
	}

	sort.Slice(instances, func(i, j int) bool {
		return instances[i].name < instances[j].name
	})

	return instances, nil
}
