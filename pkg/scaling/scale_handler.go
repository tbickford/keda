package scaling

import (
	"context"
	"fmt"
	"sync"

	kedav1alpha1 "github.com/kedacore/keda/pkg/apis/keda/v1alpha1"
	"github.com/kedacore/keda/pkg/scalers"
	"github.com/kedacore/keda/pkg/scaling/executor"
	"knative.dev/pkg/apis/duck"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/scale"
	duckv1 "knative.dev/pkg/apis/duck/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// ScaleHandler encapsulates the logic of calling the right scalers for
// each ScaledObject and making the final scale decision and operation
type ScaleHandler interface {
	HandleScalableObject(scalableObject interface{}) error
	DeleteScalableObject(scalableObject interface{}) error
	GetScalers(scalableObject interface{}) ([]scalers.Scaler, error)
}

type scaleHandler struct {
	client            client.Client
	logger            logr.Logger
	scaleLoopContexts *sync.Map
	scaleExecutor     executor.ScaleExecutor
}

const (
	// Default polling interval for a ScaledObject triggers if no pollingInterval is defined.
	defaultPollingInterval = 30
)

// NewScaleHandler creates a ScaleHandler object
func NewScaleHandler(client client.Client, scaleClient *scale.ScalesGetter, reconcilerScheme *runtime.Scheme) ScaleHandler {
	return &scaleHandler{
		client:            client,
		logger:            logf.Log.WithName("scalehandler"),
		scaleLoopContexts: &sync.Map{},
		scaleExecutor:     executor.NewScaleExecutor(client, scaleClient, reconcilerScheme),
	}
}

func (h *scaleHandler) GetScalers(scalableObject interface{}) ([]scalers.Scaler, error) {
	withTriggers, err := asDuckWithTriggers(scalableObject)
	if err != nil {
		return nil, err
	}

	withPods, containerName, err := h.resolvePods(scalableObject)
	if err != nil {
		return nil, err
	}

	return h.getScalers(withTriggers, withPods, containerName)
}

func asDuckWithTriggers(scalableObject interface{}) (*kedav1alpha1.WithTriggers, error) {
	withTriggers := &kedav1alpha1.WithTriggers{}
	switch obj := scalableObject.(type) {
	case *kedav1alpha1.ScaledObject:
	withTriggers = &kedav1alpha1.WithTriggers{
			Spec: kedav1alpha1.WithTriggersSpec{
				PollingInterval: obj.Spec.PollingInterval,
				Triggers: obj.Spec.Triggers,
			},
		}
	case *kedav1alpha1.ScaledJob:
		withTriggers = &kedav1alpha1.WithTriggers{
			Spec: kedav1alpha1.WithTriggersSpec{
				PollingInterval: obj.Spec.PollingInterval,
				Triggers: obj.Spec.Triggers,
			},
		}
	default:
		// here could be the conversion from unknown Duck type potentially in the future
		return nil, fmt.Errorf("unknown scalable object type %v", scalableObject)
	}
	return withTriggers, nil
}

func (h *scaleHandler) resolveContainerEnv(podSpec *corev1.PodSpec, containerName string, namespace string) (map[string]string, error) {

	if len(podSpec.Containers) < 1 {
		return nil, fmt.Errorf("Target object doesn't have containers")
	}

	var container corev1.Container
	if containerName != "" {
		for _, c := range podSpec.Containers {
			if c.Name == containerName {
				container = c
				break
			}
		}

		if &container == nil {
			return nil, fmt.Errorf("Couldn't find container with name %s on Target object", containerName)
		}
	} else {
		container = podSpec.Containers[0]
	}

	return h.resolveEnv(&container, namespace)
}

func (h *scaleHandler) resolveEnv(container *corev1.Container, namespace string) (map[string]string, error) {
	resolved := make(map[string]string)

	if container.EnvFrom != nil {
		for _, source := range container.EnvFrom {
			if source.ConfigMapRef != nil {
				if configMap, err := h.resolveConfigMap(source.ConfigMapRef, namespace); err == nil {
					for k, v := range configMap {
						resolved[k] = v
					}
				} else if source.ConfigMapRef.Optional != nil && *source.ConfigMapRef.Optional {
					// ignore error when ConfigMap is marked as optional
					continue
				} else {
					return nil, fmt.Errorf("error reading config ref %s on namespace %s/: %s", source.ConfigMapRef, namespace, err)
				}
			} else if source.SecretRef != nil {
				if secretsMap, err := h.resolveSecretMap(source.SecretRef, namespace); err == nil {
					for k, v := range secretsMap {
						resolved[k] = v
					}
				} else if source.SecretRef.Optional != nil && *source.SecretRef.Optional {
					// ignore error when Secret is marked as optional
					continue
				} else {
					return nil, fmt.Errorf("error reading secret ref %s on namespace %s: %s", source.SecretRef, namespace, err)
				}
			}
		}

	}

	if container.Env != nil {
		for _, envVar := range container.Env {
			var value string
			var err error

			// env is either a name/value pair or an EnvVarSource
			if envVar.Value != "" {
				value = envVar.Value
			} else if envVar.ValueFrom != nil {
				// env is an EnvVarSource, that can be on of the 4 below
				if envVar.ValueFrom.SecretKeyRef != nil {
					// env is a secret selector
					value, err = h.resolveSecretValue(envVar.ValueFrom.SecretKeyRef, envVar.ValueFrom.SecretKeyRef.Key, namespace)
					if err != nil {
						return nil, fmt.Errorf("error resolving secret name %s for env %s in namespace %s",
							envVar.ValueFrom.SecretKeyRef,
							envVar.Name,
							namespace)
					}
				} else if envVar.ValueFrom.ConfigMapKeyRef != nil {
					// env is a configMap selector
					value, err = h.resolveConfigValue(envVar.ValueFrom.ConfigMapKeyRef, envVar.ValueFrom.ConfigMapKeyRef.Key, namespace)
					if err != nil {
						return nil, fmt.Errorf("error resolving config %s for env %s in namespace %s",
							envVar.ValueFrom.ConfigMapKeyRef,
							envVar.Name,
							namespace)
					}
				} else {
					h.logger.V(1).Info("cannot resolve env %s to a value. fieldRef and resourceFieldRef env are skipped", envVar.Name)
					continue
				}

			}
			resolved[envVar.Name] = value
		}

	}

	return resolved, nil
}

func (h *scaleHandler) resolveConfigMap(configMapRef *corev1.ConfigMapEnvSource, namespace string) (map[string]string, error) {
	configMap := &corev1.ConfigMap{}
	err := h.client.Get(context.TODO(), types.NamespacedName{Name: configMapRef.Name, Namespace: namespace}, configMap)
	if err != nil {
		return nil, err
	}
	return configMap.Data, nil
}

func (h *scaleHandler) resolveSecretMap(secretMapRef *corev1.SecretEnvSource, namespace string) (map[string]string, error) {
	secret := &corev1.Secret{}
	err := h.client.Get(context.TODO(), types.NamespacedName{Name: secretMapRef.Name, Namespace: namespace}, secret)
	if err != nil {
		return nil, err
	}

	secretsStr := make(map[string]string)
	for k, v := range secret.Data {
		secretsStr[k] = string(v)
	}
	return secretsStr, nil
}

func (h *scaleHandler) resolveSecretValue(secretKeyRef *corev1.SecretKeySelector, keyName, namespace string) (string, error) {
	secret := &corev1.Secret{}
	err := h.client.Get(context.TODO(), types.NamespacedName{Name: secretKeyRef.Name, Namespace: namespace}, secret)
	if err != nil {
		return "", err
	}
	return string(secret.Data[keyName]), nil

}

func (h *scaleHandler) resolveConfigValue(configKeyRef *corev1.ConfigMapKeySelector, keyName, namespace string) (string, error) {
	configMap := &corev1.ConfigMap{}
	err := h.client.Get(context.TODO(), types.NamespacedName{Name: configKeyRef.Name, Namespace: namespace}, configMap)
	if err != nil {
		return "", err
	}
	return string(configMap.Data[keyName]), nil
}

func closeScalers(scalers []scalers.Scaler) {
	for _, scaler := range scalers {
		defer scaler.Close()
	}
}

func (h *scaleHandler) resolveAuthSecret(name, namespace, key string) string {
	if name == "" || namespace == "" || key == "" {
		h.logger.Error(fmt.Errorf("Error trying to get secret"), "name, namespace and key are required", "Secret.Namespace", namespace, "Secret.Name", name, "key", key)
		return ""
	}

	secret := &corev1.Secret{}
	err := h.client.Get(context.TODO(), types.NamespacedName{Name: name, Namespace: namespace}, secret)
	if err != nil {
		h.logger.Error(err, "Error trying to get secret from namespace", "Secret.Namespace", namespace, "Secret.Name", name)
		return ""
	}
	result := secret.Data[key]

	if result == nil {
		return ""
	}

	return string(result)
}

func (h *scaleHandler) parseAuthRef(triggerAuthRef *kedav1alpha1.ScaledObjectAuthRef, withTriggers *kedav1alpha1.WithTriggers, podSpec *corev1.PodSpec) (map[string]string, string) {
	result := make(map[string]string)
	podIdentity := ""

	if triggerAuthRef != nil && triggerAuthRef.Name != "" {
		triggerAuth := &kedav1alpha1.TriggerAuthentication{}
		err := h.client.Get(context.TODO(), types.NamespacedName{Name: triggerAuthRef.Name, Namespace: withTriggers.Namespace}, triggerAuth)
		if err != nil {
			h.logger.Error(err, "Error getting triggerAuth", "triggerAuthRef.Name", triggerAuthRef.Name)
		} else {
			podIdentity = string(triggerAuth.Spec.PodIdentity.Provider)
			if triggerAuth.Spec.Env != nil {
				for _, e := range triggerAuth.Spec.Env {
					env, err := h.resolveContainerEnv(podSpec, e.ContainerName, withTriggers.Namespace)
					if err != nil {
						result[e.Parameter] = ""
					} else {
						result[e.Parameter] = env[e.Name]
					}
				}
			}
			if triggerAuth.Spec.SecretTargetRef != nil {
				for _, e := range triggerAuth.Spec.SecretTargetRef {
					result[e.Parameter] = h.resolveAuthSecret(e.Name, withTriggers.Namespace, e.Key)
				}
			}
		}
	}

	return result, podIdentity
}

func (h *scaleHandler) resolvePods(scalableObject interface{}) (*duckv1.WithPod, string, error) {
	switch obj := scalableObject.(type) {
	case *kedav1alpha1.ScaledObject:
		unstruct := &unstructured.Unstructured{}
		unstruct.SetGroupVersionKind(obj.Status.ScaleTargetGVKR.GroupVersionKind())
		if err := h.client.Get(context.TODO(), client.ObjectKey{Namespace: obj.Namespace, Name: obj.Spec.ScaleTargetRef.Name}, unstruct); err != nil {
			// resource doesn't exist
			h.logger.Error(err, "Target resource doesn't exist", "resource", obj.Status.ScaleTargetGVKR.GVKString(), "name", obj.Spec.ScaleTargetRef.Name)
			return nil, "", err
		}

		withPods := &duckv1.WithPod{}
		if err := duck.FromUnstructured(unstruct, withPods); err != nil {
			h.logger.Error(err, "Cannot convert unstructured into PodSpecable Duck-type", "object", unstruct)
		}

		if withPods.Spec.Template.Spec.Containers == nil {
			h.logger.Info("There aren't any containers in the ScaleTarget", "resource", obj.Status.ScaleTargetGVKR.GVKString(), "name", obj.Spec.ScaleTargetRef.Name)
			return nil, "", fmt.Errorf("No containers found")
		}

		return withPods, obj.Spec.ScaleTargetRef.ContainerName, nil
	}

	// TODO: implement this for ScaledJobs!! 
	return nil, "", fmt.Errorf("resolvePods is only implemented for ScaledObjects so far")
}

// GetScaledObjectScalers returns list of Scalers for the specified ScaledObject
func (h *scaleHandler) getScalers(scalableType *kedav1alpha1.WithTriggers, withPods *duckv1.WithPod, containerName string) ([]scalers.Scaler, error) {
	scalersRes := []scalers.Scaler{}

	resolvedEnv, err := h.resolveContainerEnv(&withPods.Spec.Template.Spec, containerName, scalableType.Namespace)
	if err != nil {
		return scalersRes, fmt.Errorf("error resolving secrets for ScaleTarget: %s", err)
	}

	for i, trigger := range scalableType.Spec.Triggers {
		authParams, podIdentity := h.parseAuthRef(trigger.AuthenticationRef, scalableType, &withPods.Spec.Template.Spec)

		if podIdentity == kedav1alpha1.PodIdentityProviderAwsEKS {
			serviceAccountName := withPods.Spec.Template.Spec.ServiceAccountName
			serviceAccount := &corev1.ServiceAccount{}
			err = h.client.Get(context.TODO(), types.NamespacedName{Name: serviceAccountName, Namespace: scalableType.Namespace}, serviceAccount)
			if err != nil {
				closeScalers(scalersRes)
				return []scalers.Scaler{}, fmt.Errorf("error getting service account: %s", err)
			}
			authParams["awsRoleArn"] = serviceAccount.Annotations[kedav1alpha1.PodIdentityAnnotationEKS]
		} else if podIdentity == kedav1alpha1.PodIdentityProviderAwsKiam {
			authParams["awsRoleArn"] = withPods.Spec.Template.ObjectMeta.Annotations[kedav1alpha1.PodIdentityAnnotationKiam]
		}

		scaler, err := h.getScaler(scalableType.Name, scalableType.Namespace, trigger.Type, resolvedEnv, trigger.Metadata, authParams, podIdentity)
		if err != nil {
			closeScalers(scalersRes)
			return []scalers.Scaler{}, fmt.Errorf("error getting scaler for trigger #%d: %s", i, err)
		}

		scalersRes = append(scalersRes, scaler)
	}

	return scalersRes, nil
}

func (h *scaleHandler) getScaler(name, namespace, triggerType string, resolvedEnv, triggerMetadata, authParams map[string]string, podIdentity string) (scalers.Scaler, error) {
	switch triggerType {
	case "azure-queue":
		return scalers.NewAzureQueueScaler(resolvedEnv, triggerMetadata, authParams, podIdentity)
	case "azure-servicebus":
		return scalers.NewAzureServiceBusScaler(resolvedEnv, triggerMetadata, authParams, podIdentity)
	case "aws-sqs-queue":
		return scalers.NewAwsSqsQueueScaler(resolvedEnv, triggerMetadata, authParams)
	case "aws-cloudwatch":
		return scalers.NewAwsCloudwatchScaler(resolvedEnv, triggerMetadata, authParams)
	case "aws-kinesis-stream":
		return scalers.NewAwsKinesisStreamScaler(resolvedEnv, triggerMetadata, authParams)
	case "kafka":
		return scalers.NewKafkaScaler(resolvedEnv, triggerMetadata, authParams)
	case "rabbitmq":
		return scalers.NewRabbitMQScaler(resolvedEnv, triggerMetadata, authParams)
	case "azure-eventhub":
		return scalers.NewAzureEventHubScaler(resolvedEnv, triggerMetadata)
	case "prometheus":
		return scalers.NewPrometheusScaler(resolvedEnv, triggerMetadata)
	case "redis":
		return scalers.NewRedisScaler(resolvedEnv, triggerMetadata, authParams)
	case "gcp-pubsub":
		return scalers.NewPubSubScaler(resolvedEnv, triggerMetadata)
	case "external":
		return scalers.NewExternalScaler(name, namespace, resolvedEnv, triggerMetadata)
	case "liiklus":
		return scalers.NewLiiklusScaler(resolvedEnv, triggerMetadata)
	case "stan":
		return scalers.NewStanScaler(resolvedEnv, triggerMetadata)
	case "huawei-cloudeye":
		return scalers.NewHuaweiCloudeyeScaler(triggerMetadata, authParams)
	case "azure-blob":
		return scalers.NewAzureBlobScaler(resolvedEnv, triggerMetadata, authParams, podIdentity)
	case "postgresql":
		return scalers.NewPostgreSQLScaler(resolvedEnv, triggerMetadata, authParams)
	case "mysql":
		return scalers.NewMySQLScaler(resolvedEnv, triggerMetadata, authParams)
	case "azure-monitor":
		return scalers.NewAzureMonitorScaler(resolvedEnv, triggerMetadata, authParams)
	default:
		return nil, fmt.Errorf("no scaler found for type: %s", triggerType)
	}
}
