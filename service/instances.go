package service

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/foursquare/fsgo/net/discovery"
)

// Instances is a custom slice type that stores ServiceInstances.
// It also provides a simple API that clients can use when building
// other data structures.
type Instances []*discovery.ServiceInstance

// Len() is a shortcut for len(this)
func (this Instances) Len() int {
	return len(this)
}

// String outputs a string representation of this Instances, useful for debugging.
// This method follows pointers to make the debug output more useful.
func (this Instances) String() string {
	var output bytes.Buffer
	output.WriteRune('[')
	initialLength := output.Len()

	for _, serviceInstance := range this {
		if output.Len() > initialLength {
			output.WriteRune(',')
		}

		output.WriteString(fmt.Sprintf("%#v", *serviceInstance))
	}

	output.WriteRune(']')
	return output.String()
}

// RegisterWith registers each instance in this slice with the supplied service discovery.
// This method normalizes each ServiceInstance, using the discovery API to create a new instance
// with internal data members set (e.g. timestamps).
func (this Instances) RegisterWith(serviceDiscovery *discovery.ServiceDiscovery) error {
	for _, original := range this {
		normalized := discovery.NewServiceInstance(
			original.Name,
			original.Address,
			original.Port,
			original.SslPort,
			original.Payload,
		)

		err := serviceDiscovery.Register(normalized)
		if err != nil {
			return errors.New(
				fmt.Sprintf("Error while registering service instance %v: %v", normalized, err),
			)
		}
	}

	return nil
}

// ToKeys maps each ServiceInstance onto a string key via keyFunc, then
// invokes Keys.Add() for each key.
func (this Instances) ToKeys(keyFunc KeyFunc, output Keys) {
	for _, serviceInstance := range this {
		output.Add(keyFunc(serviceInstance))
	}
}

// ToKeyMap maps each ServiceInstance onto a string key as in ToKeys,
// but both the key and the ServiceInstance value are stored in the output.
func (this Instances) ToKeyMap(keyFunc KeyFunc, output KeyMap) {
	for _, serviceInstance := range this {
		output[keyFunc(serviceInstance)] = serviceInstance
	}
}
