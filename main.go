package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"sync"
	"syscall"
	"time"

	"github.com/rjeczalik/notify"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	if err := Main(); err != nil {
		log.Println(err.Error())
		os.Exit(1)
	}
}

var (
	kubeConfigPath = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	kubeMasterURL  = flag.String("master", "", "master url")
	namespace      = flag.String("namespace", "default", "The kubernetes namespace")
	objectName     = flag.String("secret", "", "The name of the secret")
	localPath      = flag.String("path", "", "The path to the secret")

	// resyncPeriod is how often we get updates of the k8s state even if nothing has changed
	resyncPeriod = 30 * time.Second
	kubeClient   *kubernetes.Clientset
)

// Main is the entrypoint for the program
func Main() error {
	flag.Parse()
	if *objectName == "" {
		return fmt.Errorf("You must specify a secret object")
	}
	if *localPath == "" {
		return fmt.Errorf("You must specify a local path")
	}

	config, err := clientcmd.BuildConfigFromFlags(*kubeMasterURL, *kubeConfigPath)
	if err != nil {
		return fmt.Errorf("cannot create k8s config: %v", err)
	}
	kubeClient, err = kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("cannot create k8s client: %v", err)
	}

	s := Command{}
	if err := s.Start(kubeClient); err != nil {
		return err
	}

	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, os.Interrupt, syscall.SIGTERM)
	<-signalCh

	s.Stop()
	return nil
}

// Command implements kubersync
type Command struct {
	Secrets cache.Store

	updateMu   sync.Mutex
	haveSynced bool
	stopCh     chan struct{}
}

// Start starts the Command.
func (s *Command) Start(kubeClient *kubernetes.Clientset) error {
	s.stopCh = make(chan struct{})

	var secretsController cache.Controller

	// watch for Secret changes
	{
		// TODO(ross): how do we filter the watch to contain only the object
		//   we care about and not all the objects?
		listWatch := cache.NewListWatchFromClient(kubeClient.Core().RESTClient(), "secrets", *namespace, fields.Everything())
		eventHandler := cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				s.onAdd(obj.(*v1.Secret))
			},
			UpdateFunc: func(oldObj, newObj interface{}) {
				s.onUpdate(oldObj.(*v1.Secret), newObj.(*v1.Secret))
			},
			DeleteFunc: func(obj interface{}) {
				s.onDelete(obj.(*v1.Secret))
			},
		}

		s.Secrets, secretsController = cache.NewInformer(listWatch, &v1.Secret{}, resyncPeriod, eventHandler)
		go secretsController.Run(s.stopCh)
	}

	fmt.Println("loading")
	if !cache.WaitForCacheSync(
		s.stopCh,
		secretsController.HasSynced,
	) {
		return fmt.Errorf("timed out waiting for caches to sync")
	}

	s.haveSynced = true
	s.onFileChanged()

	fmt.Println("ready")
	go s.watchFileChanges()
	return nil
}

// Stop stops the Command.
func (s *Command) Stop() {
	close(s.stopCh)
}

func (s *Command) onAdd(new *v1.Secret) {
	if new.ObjectMeta.Name != *objectName {
		return
	}

	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	s.update(new)
}

func (s *Command) onUpdate(old, new *v1.Secret) {
	if new.ObjectMeta.Name != *objectName {
		return
	}

	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	s.update(new)
}

func (s *Command) onDelete(old *v1.Secret) {
	if old.ObjectMeta.Name != *objectName {
		return
	}

	// if the secret object gets deleted, we want to crash
	// rather than screw up the state somehow
	close(s.stopCh)
}

func (s *Command) update(new *v1.Secret) error {
	// any existing files will be deleted, unless they are
	// included in the secret
	filesToDelete := map[string]bool{}
	filepath.Walk(*localPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		filesToDelete[path] = true
		return nil
	})

	for path, data := range new.Data {
		path := filepath.Join(*localPath, path)
		filesToDelete[path] = false
		if err := s.writeFile(path, []byte(data)); err != nil {
			return err
		}

		// TODO(ross): set file mode and permissions
		// os.Chmod(path, 0640)
		// group, _ := user.LookupGroup("www-data")
		// if group != nil {
		// 	gid, _ := strconv.Atoi(group.Gid)
		// 	os.Chown(path, 0, gid)
		// }
	}

	if s.haveSynced {
		for path, shouldDelete := range filesToDelete {
			if !shouldDelete {
				continue
			}
			fmt.Println("delete", path)
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}

	return nil
}

func (s *Command) writeFile(path string, contents []byte) (err error) {
	os.MkdirAll(filepath.Dir(path), 0755)
	current, _ := ioutil.ReadFile(path)
	if bytes.Equal(contents, current) {
		return nil
	}

	fmt.Println("write", path)
	return ioutil.WriteFile(path, contents, 0644)
}

// watchFileChanges invokes onFileChanged every time a file changes
func (s *Command) watchFileChanges() error {
	c := make(chan notify.EventInfo, 1000)
	if err := notify.Watch(filepath.Join(*localPath, "..."), c, notify.All); err != nil {
		return err
	}
	defer notify.Stop(c)

	for {
		select {
		case ei := <-c:
			fmt.Println(ei)
			if err := s.onFileChanged(); err != nil {
				fmt.Fprintln(os.Stderr, err.Error())
			}
		case _ = <-s.stopCh:
			return nil
		}
	}
}

func (s *Command) onFileChanged() error {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()

	obj, exists, err := s.Secrets.GetByKey(*namespace + "/" + *objectName)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	secret := obj.(*v1.Secret)
	oldData := secret.Data
	secret.Data = map[string][]byte{}

	if err := filepath.Walk(*localPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		current, err := ioutil.ReadFile(path)
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(*localPath, path)
		if err != nil {
			return err
		}
		secret.Data[relPath] = current
		return nil
	}); err != nil {
		return err
	}

	if reflect.DeepEqual(oldData, secret.Data) {
		return nil
	}

	if _, err := kubeClient.Core().Secrets(*namespace).Update(secret); err != nil {
		return err
	}

	fmt.Println("updated secret")
	return nil
}
