package scaling

import (
	"context"
	"fmt"
	"time"

	kedav1alpha1 "github.com/kedacore/keda/pkg/apis/keda/v1alpha1"
)

func (h *scaleHandler) HandleScalableObject(scalableObject interface{}) error {
	withTriggers, err := asDuckWithTriggers(scalableObject)
	if err != nil {
		h.logger.Error(err, "error duck typing object into withTrigger")
		return err
	}

	key := generateKey(withTriggers)

	ctx, cancel := context.WithCancel(context.TODO())

	// cancel the outdated ScaleLoop for the same ScaledObject (if exists)
	value, loaded := h.scaleLoopContexts.LoadOrStore(key, cancel)
	if loaded {
		cancelValue, ok := value.(context.CancelFunc)
		if ok {
			cancelValue()
		}
		h.scaleLoopContexts.Store(key, cancel)
	}

	go h.startScaleLoop(ctx, withTriggers, scalableObject)
	return nil
}

func (h *scaleHandler) DeleteScalableObject(scalableObject interface{}) error {
	withTriggers, err := asDuckWithTriggers(scalableObject)
	if err != nil {
		h.logger.Error(err, "error duck typing object into withTrigger")
		return err
	}

	key := generateKey(withTriggers)

	result, ok := h.scaleLoopContexts.Load(key)
	if ok {
		cancel, ok := result.(context.CancelFunc)
		if ok {
			cancel()
		}
		h.scaleLoopContexts.Delete(key)
	} else {
		h.logger.V(1).Info("ScaleObject was not found in controller cache", "key", key)
	}

	return nil
}

func getPollingInterval(withTriggers *kedav1alpha1.WithTriggers) time.Duration {
	if withTriggers.Spec.PollingInterval != nil {
		return time.Second * time.Duration(*withTriggers.Spec.PollingInterval)
	}

	return time.Second * time.Duration(defaultPollingInterval)
}

// startScaleLoop blocks forever and checks the scaledObject based on its pollingInterval
func (h *scaleHandler) startScaleLoop(ctx context.Context, withTriggers *kedav1alpha1.WithTriggers, scalableObject interface{}) {
	logger := h.logger.WithValues("namespace", withTriggers.GetNamespace(), "name", withTriggers.GetName())

	// kick off one check to the scalers now
	h.checkScalers(ctx, withTriggers, scalableObject)

	pollingInterval := getPollingInterval(withTriggers)
	logger.V(1).Info("Watching with pollingInterval", "PollingInterval", pollingInterval)

	for {
		select {
		case <-time.After(pollingInterval):
			h.checkScalers(ctx, withTriggers, scalableObject)
		case <-ctx.Done():
			logger.V(1).Info("Context canceled")
			return
		}
	}
}

// checkScalers contains the main logic for the ScaleHandler scaling logic.
// It'll check each trigger active status then call RequestScale
func (h *scaleHandler) checkScalers(ctx context.Context, withTriggers *kedav1alpha1.WithTriggers, scalableObject interface{}) {
	withPods, containerName, err := h.resolvePods(scalableObject)
	if err != nil {
		h.logger.Error(err, "Error resolving pods")
		return
	}

	scalers, err := h.getScalers(withTriggers, withPods, containerName)
	if err != nil {
		h.logger.Error(err, "Error getting scalers")
		return
	}

	switch obj := scalableObject.(type) {
	case *kedav1alpha1.ScaledObject:
		h.scaleExecutor.RequestScale(ctx, scalers, obj)
	case *kedav1alpha1.ScaledJob:
		h.scaleExecutor.RequestJobScale(ctx, scalers, obj)
	}
}

func generateKey(scalableObject *kedav1alpha1.WithTriggers) string {
	return fmt.Sprintf("%s.%s.%s", scalableObject.Kind, scalableObject.Namespace, scalableObject.Name)
}
