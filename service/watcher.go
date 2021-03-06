package service

import (
	"errors"
	"fmt"
	"github.com/foursquare/curator.go"
	"github.com/foursquare/fsgo/net/discovery"
	"github.com/samuel/go-zookeeper/zk"
	"sync"
)

// serviceWatcher holds meta data about one particular service that's being
// observed for changes.  This type also implements a simple API for interacting
// with Zookeeper.
type serviceWatcher struct {
	curatorConnection  discovery.Conn
	instanceSerializer discovery.InstanceSerializer
	servicePath        string
	serviceName        string
	logger             zk.Logger

	listenerMutex sync.Mutex
	listeners     []Listener
}

// addListener appends a listener to this watcher
func (this *serviceWatcher) addListener(listener Listener) {
	this.listenerMutex.Lock()
	defer this.listenerMutex.Unlock()
	this.listeners = append(this.listeners, listener)
}

// removeListener removes a listener to this watcher
func (this *serviceWatcher) removeListener(listener Listener) bool {
	this.listenerMutex.Lock()
	defer this.listenerMutex.Unlock()
	for index, candidate := range this.listeners {
		if candidate == listener {
			this.listeners = append(this.listeners[:index], this.listeners[index+1:]...)
			return true
		}
	}

	return false
}

// dispatch broadcasts the given service Instances to all listeners associated
// with this watcher
func (this *serviceWatcher) dispatch(instances Instances) {
	this.listenerMutex.Lock()
	defer this.listenerMutex.Unlock()
	for _, listener := range this.listeners {
		listener.ServicesChanged(this.serviceName, instances)
	}
}

// fetchServices obtains the ServiceInstance objects from the given slice
// of child nodes.  This method is tolerant of zookeeper and parsing errors,
// since during network flapping it's possible that the slice of child ids
// is no longer valid.  This will be reflected in a partially filled or empty
// Instances result.
func (this *serviceWatcher) fetchServices(childIds []string) Instances {
	this.logger.Printf("fetchServices(childIds=%s)", childIds)
	instances := make(Instances, 0, len(childIds))

	for _, childId := range childIds {
		instancePath := this.servicePath + "/" + childId
		this.logger.Printf("Obtaining data for znode: %s", instancePath)
		data, err := this.curatorConnection.GetData().ForPath(instancePath)
		if err != nil {
			// ignore errors when obtaining the child data, as its possible for the
			// current set of children to have changed before this method was called
			this.logger.Printf("Error retrieving data from %s: %s", instancePath, err)
			continue
		}

		serviceInstance, err := this.instanceSerializer.Deserialize(data)
		if err != nil {
			// ignore deserialization errors, as it's possible when doing upgrades
			// for multiple versions of the discovery client to run simultaneously
			this.logger.Printf("Error deserializing service instance from %s: %s", instancePath, err)
			continue
		}

		serviceInstance.Id = childId
		instances = append(instances, serviceInstance)
	}

	return instances
}

// readServices obtains the current child nodes, then invokes readServices
func (this *serviceWatcher) readServices() (Instances, error) {
	this.logger.Printf("readServices() [servicePath=%s]", this.servicePath)
	childIds, err := this.curatorConnection.GetChildren().ForPath(this.servicePath)
	if err != nil {
		return nil, errors.New(
			fmt.Sprintf("Error while fetching children for path %s: %v", this.servicePath, err),
		)
	}

	return this.fetchServices(childIds), nil
}

// readServicesAndWatch is like readServices, except that it also sets a watch
// on the watched service path
func (this *serviceWatcher) readServicesAndWatch() (Instances, error) {
	this.logger.Printf("readServicesAndWatch() [servicePath=%s]", this.servicePath)
	childIds, err := this.curatorConnection.GetChildren().
		Watched().
		ForPath(this.servicePath)
	if err != nil {
		return nil, errors.New(
			fmt.Sprintf("Error while getting children with watch for path %s: %v", this.servicePath, err),
		)
	}

	return this.fetchServices(childIds), nil
}

// setWatch simply sets a watch on the service path
func (this *serviceWatcher) setWatch() error {
	this.logger.Printf("setWatch() [servicePath=%s]", this.servicePath)
	_, err := this.curatorConnection.GetChildren().
		Watched().
		ForPath(this.servicePath)
	if err != nil {
		return errors.New(
			fmt.Sprintf("Error while setting child watch for path %s: %v", this.servicePath, err),
		)
	}

	return nil
}

// initialize sets up this watcher with a curator connection and ensures that any necessary
// znode paths exist.  The initial set of services is dispatched to any listeners.
func (this *serviceWatcher) initialize(curatorConnection discovery.Conn) error {
	this.logger.Printf("initialize(curatorConnection=%v)", curatorConnection)
	this.curatorConnection = curatorConnection

	this.logger.Printf("Ensuring %s exists ...", this.servicePath)
	err := curator.NewEnsurePath(this.servicePath).Ensure(this.curatorConnection.ZookeeperClient())
	if err != nil && err != zk.ErrNodeExists {
		return errors.New(
			fmt.Sprintf("Error during initialization while ensuring path %s: %v", this.servicePath, err),
		)
	}

	this.listenerMutex.Lock()
	defer this.listenerMutex.Unlock()

	if len(this.listeners) > 0 {
		instances, err := this.readServicesAndWatch()
		if err != nil {
			return err
		}

		// manually dispatch to listeners, since locks are reentrant
		for _, listener := range this.listeners {
			listener.ServicesChanged(this.serviceName, instances)
		}
	}

	return this.setWatch()
}

// serviceWatcherSet is an internal collection type that maps serviceWatches by name and path
type serviceWatcherSet struct {
	serviceNames []string
	byName       map[string]*serviceWatcher
	byPath       map[string]*serviceWatcher
	logger       zk.Logger
}

// newServiceWatcherSet is an internal Factory Method that creates one serviceWatcher
// for each service name, then returns a serviceWatcherSet with the services mapped.
func newServiceWatcherSet(logger zk.Logger, serviceNames []string, basePath string) *serviceWatcherSet {
	logger.Printf("newServiceWatcherSet(serviceNames=%s, basePath=%s)", serviceNames, basePath)
	watcherCount := len(serviceNames)
	byName := make(map[string]*serviceWatcher, watcherCount)
	byPath := make(map[string]*serviceWatcher, watcherCount)
	instanceSerializer := &discovery.JsonInstanceSerializer{}

	for _, serviceName := range serviceNames {
		// ignore duplicate service names
		if _, ok := byName[serviceName]; ok {
			logger.Printf("Skipping duplicate watched service name: %s", serviceName)
			continue
		}

		servicePath := basePath + "/" + serviceName
		serviceWatcher := &serviceWatcher{
			instanceSerializer: instanceSerializer,
			servicePath:        servicePath,
			serviceName:        serviceName,
			logger:             logger,
		}

		byName[serviceWatcher.serviceName] = serviceWatcher
		byPath[serviceWatcher.servicePath] = serviceWatcher
	}

	serviceWatcherSet := &serviceWatcherSet{
		serviceNames: make([]string, len(byName)),
		byName:       byName,
		byPath:       byPath,
		logger:       logger,
	}

	// copying the keys ensures that the service names have been deduped
	index := 0
	for serviceName := range serviceWatcherSet.byName {
		serviceWatcherSet.serviceNames[index] = serviceName
		index++
	}

	logger.Printf("using serviceWatcherSet: %v", serviceWatcherSet)
	return serviceWatcherSet
}

func (this *serviceWatcherSet) serviceCount() int {
	return len(this.serviceNames)
}

func (this *serviceWatcherSet) cloneServiceNames() []string {
	serviceNames := make([]string, len(this.serviceNames))
	copy(serviceNames, this.serviceNames)
	return serviceNames
}

func (this *serviceWatcherSet) findByName(serviceName string) (*serviceWatcher, bool) {
	value, ok := this.byName[serviceName]
	return value, ok
}

func (this *serviceWatcherSet) findByPath(path string) (*serviceWatcher, bool) {
	value, ok := this.byPath[path]
	return value, ok
}

// initialize initializes all watchers in this set
func (this *serviceWatcherSet) initialize(curatorConnection discovery.Conn) error {
	this.logger.Printf("initialize(curatorConnection=%v)", curatorConnection)
	for _, serviceWatcher := range this.byName {
		err := serviceWatcher.initialize(curatorConnection)
		if err != nil {
			this.logger.Printf("Error initializing service watcher %v: %s", serviceWatcher, err)
			return err
		}
	}

	return nil
}
