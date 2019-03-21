// Copyright 2014 mqant Author. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package defaultApp

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/liangdas/mqant/conf"
	"github.com/liangdas/mqant/log"
	"github.com/liangdas/mqant/module"
	"github.com/liangdas/mqant/module/base"
	"github.com/liangdas/mqant/module/modules"
	"hash/crc32"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"github.com/liangdas/mqant/registry"
	"github.com/nats-io/go-nats"
	"github.com/liangdas/mqant/selector"
	"github.com/liangdas/mqant/selector/cache"
	"sync"
)

type resultInfo struct {
	Trace  string
	Error  string      //错误结果 如果为nil表示请求正确
	Result interface{} //结果
}

type protocolMarshalImp struct {
	data []byte
}

func (this *protocolMarshalImp) GetData() []byte {
	return this.data
}

func NewApp(version string) module.App {
	app := new(DefaultApp)
	app.routes = map[string]func(app module.App, Type string, hash string) module.ServerSession{}
	app.defaultRoutes = func(app module.App, Type string, hash string) module.ServerSession {
		//默认使用第一个Server
		servers := app.GetServersByType(Type)
		if len(servers) == 0 {
			return nil
		}
		index := int(math.Abs(float64(crc32.ChecksumIEEE([]byte(hash))))) % len(servers)
		return servers[index]
	}
	app.rpcserializes = map[string]module.RPCSerialize{}
	app.version = version
	return app
}

type DefaultApp struct {
	//module.App
	version       string
	settings      conf.Config
	serverList    sync.Map
	processId     string
	nc 		*nats.Conn
	selector 	selector.Selector
	routes        map[string]func(app module.App, Type string, hash string) module.ServerSession
	defaultRoutes func(app module.App, Type string, hash string) module.ServerSession
	//将一个RPC调用路由到新的路由上
	mapRoute            func(app module.App, route string) string
	rpcserializes       map[string]module.RPCSerialize
	configurationLoaded func(app module.App)
	startup             func(app module.App)
	moduleInited        func(app module.App, module module.Module)
	protocolMarshal     func(Trace string, Result interface{}, Error string) (module.ProtocolMarshal, string)
}

func (app *DefaultApp) Run(debug bool, mods ...module.Module) error {
	wdPath := flag.String("wd", "", "Server work directory")
	confPath := flag.String("conf", "", "Server configuration file path")
	ProcessID := flag.String("pid", "development", "Server ProcessID?")
	Logdir := flag.String("log", "", "Log file directory?")
	BIdir := flag.String("bi", "", "bi file directory?")
	flag.Parse() //解析输入的参数
	app.processId = *ProcessID
	ApplicationDir := ""
	if *wdPath != "" {
		_, err := os.Open(*wdPath)
		if err != nil {
			panic(err)
		}
		os.Chdir(*wdPath)
		ApplicationDir, err = os.Getwd()
	} else {
		var err error
		ApplicationDir, err = os.Getwd()
		if err != nil {
			file, _ := exec.LookPath(os.Args[0])
			ApplicationPath, _ := filepath.Abs(file)
			ApplicationDir, _ = filepath.Split(ApplicationPath)
		}

	}

	defaultConfPath := fmt.Sprintf("%s/bin/conf/server.json", ApplicationDir)
	defaultLogPath := fmt.Sprintf("%s/bin/logs", ApplicationDir)
	defaultBIPath := fmt.Sprintf("%s/bin/bi", ApplicationDir)

	if *confPath == "" {
		*confPath = defaultConfPath
	}

	if *Logdir == "" {
		*Logdir = defaultLogPath
	}

	if *BIdir == "" {
		*BIdir = defaultBIPath
	}

	f, err := os.Open(*confPath)
	if err != nil {
		panic(err)
	}

	_, err = os.Open(*Logdir)
	if err != nil {
		//文件不存在
		err := os.Mkdir(*Logdir, os.ModePerm) //
		if err != nil {
			fmt.Println(err)
		}
	}

	_, err = os.Open(*BIdir)
	if err != nil {
		//文件不存在
		err := os.Mkdir(*BIdir, os.ModePerm) //
		if err != nil {
			fmt.Println(err)
		}
	}
	fmt.Println("Server configuration file path :", *confPath)
	conf.LoadConfig(f.Name()) //加载配置文件
	app.Configure(conf.Conf)  //配置信息
	log.InitLog(debug, *ProcessID, *Logdir, conf.Conf.Log)
	log.InitBI(debug, *ProcessID, *BIdir, conf.Conf.BI)

	log.Info("mqant %v starting up", app.version)

	if app.configurationLoaded != nil {
		app.configurationLoaded(app)
	}

	manager := basemodule.NewModuleManager()
	manager.RegisterRunMod(modules.TimerModule()) //注册时间轮模块 每一个进程都默认运行
	// module
	for i := 0; i < len(mods); i++ {
		mods[i].OnAppConfigurationLoaded(app)
		manager.Register(mods[i])
	}
	app.OnInit(app.settings)
	manager.Init(app, *ProcessID)
	if app.startup != nil {
		app.startup(app)
	}
	// close
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, os.Kill, syscall.SIGTERM)
	sig := <-c

	//如果一分钟都关不了则强制关闭
	timeout := time.NewTimer(time.Minute)
	wait := make(chan struct{})
	go func() {
		manager.Destroy()
		app.OnDestroy()
		wait <- struct{}{}
	}()
	select {
	case <-timeout.C:
		panic(fmt.Sprintf("mqant close timeout (signal: %v)", sig))
	case <-wait:
		log.Info("mqant closing down (signal: %v)", sig)
	}
	return nil
}
func (app *DefaultApp) Route(moduleType string, fn func(app module.App, Type string, hash string) module.ServerSession) error {
	app.routes[moduleType] = fn
	return nil
}

func (app *DefaultApp) SetMapRoute(fn func(app module.App, route string) string) error {
	app.mapRoute = fn
	return nil
}

func (app *DefaultApp) getRoute(moduleType string) func(app module.App, Type string, hash string) module.ServerSession {
	fn := app.routes[moduleType]
	if fn == nil {
		//如果没有设置的路由,则使用默认的
		return app.defaultRoutes
	}
	return fn
}

func (app *DefaultApp) AddRPCSerialize(name string, Interface module.RPCSerialize) error {
	if _, ok := app.rpcserializes[name]; ok {
		return fmt.Errorf("The name(%s) has been occupied", name)
	}
	app.rpcserializes[name] = Interface
	return nil
}
func (app *DefaultApp) Transport() *nats.Conn{
	return app.nc
}
func (app *DefaultApp) GetRPCSerialize() map[string]module.RPCSerialize {
	return app.rpcserializes
}

func (app *DefaultApp) Configure(settings conf.Config) error {
	app.settings = settings
	return nil
}

/**
 */
func (app *DefaultApp) OnInit(settings conf.Config) error {
	registry.DefaultRegistry.Init(
	)
	nc, err := nats.Connect(nats.DefaultURL)
	if err != nil {
		return fmt.Errorf("nats agent: %s", err.Error())
	}
	app.nc=nc
	app.selector=cache.NewSelector(selector.Registry(registry.DefaultRegistry))
	app.selector.Init()
	return nil
}

func (app *DefaultApp) OnDestroy() error {

	return nil
}

func (app *DefaultApp) GetServerById(serverId string) (module.ServerSession, error) {
	serviceName:=serverId
	s:=strings.Split(serverId,"@")
	if len(s)==2{
		serviceName=s[0]
	}
	next,err:=app.selector.Select(serviceName)
	if err!=nil{
		return nil,err
	}
	node,err:=next()
	if err!=nil{
		return nil,err
	}
	session,ok:=app.serverList.Load(node.Id)
	if !ok{
		s ,err:= basemodule.NewServerSession(app,serviceName, node)
		if err!=nil{
			return nil,err
		}
		app.serverList.Store(node.Id,s)
		return s,nil
	}
	return session.(module.ServerSession),nil

	//services,err:=registry.DefaultRegistry.GetService(serviceName)
	//if err!=nil{
	//	return nil,err
	//}
	//for _, service := range services {
	//	//log.TInfo(nil,"GetServersByType3 %v %v",Type,service.Nodes)
	//	for _,node:=range service.Nodes{
	//		if node.Id==serverId{
	//			session ,err:= basemodule.NewServerSession(app,service, node)
	//			if err!=nil{
	//				return nil,err
	//			}
	//			return session,nil
	//		}
	//	}
	//
	//}
	//return nil,errors.Errorf("nofound node %v",serverId)
}

func (app *DefaultApp) GetServersByType(serviceName string) []module.ServerSession {
	sessions := make([]module.ServerSession, 0)
	next,err:=app.selector.Select(serviceName)
	if err!=nil{
		return sessions
	}
	node,err:=next()
	if err!=nil{
		return sessions
	}
	session,ok:=app.serverList.Load(node.Id)
	if !ok{
		s ,err:= basemodule.NewServerSession(app,serviceName, node)
		if err!=nil{
			log.Warning("NewServerSession %v",err)
		}else{
			app.serverList.Store(node.Id,s)
			sessions = append(sessions, s)
		}
	}else{
		sessions = append(sessions, session.(module.ServerSession))
	}
	//services,err:=registry.DefaultRegistry.GetService(Type)
	//if err!=nil{
	//	return sessions
	//}
	//for _, service := range services {
	//	//log.TInfo(nil,"GetServersByType3 %v %v",Type,service.Nodes)
	//	for _,node:=range service.Nodes{
	//		session ,err:= basemodule.NewServerSession(app,service, node)
	//		if err!=nil{
	//			continue
	//		}
	//		sessions = append(sessions, session)
	//	}
	//
	//}
	return sessions
}

//func (app *DefaultApp) GetServerById(serverId string) (module.ServerSession, error) {
//	if session, ok := app.serverList[serverId]; ok {
//		return session, nil
//	} else {
//		return nil, fmt.Errorf("Server(%s) Not Found", serverId)
//	}
//}
//
//func (app *DefaultApp) GetServersByType(Type string) []module.ServerSession {
//	sessions := make([]module.ServerSession, 0)
//	for _, session := range app.serverList {
//		if session.GetType() == Type {
//			sessions = append(sessions, session)
//		}
//	}
//	return sessions
//}

func (app *DefaultApp) GetRouteServer(filter string, hash string) (s module.ServerSession, err error) {
	if app.mapRoute != nil {
		//进行一次路由转换
		filter = app.mapRoute(app, filter)
	}
	sl := strings.Split(filter, "@")
	if len(sl) == 2 {
		moduleID := sl[1]
		if moduleID != "" {
			return app.GetServerById(moduleID)
		}
	}
	moduleType := sl[0]
	route := app.getRoute(moduleType)
	s = route(app, moduleType, hash)
	if s == nil {
		err = fmt.Errorf("Server(type : %s) Not Found", moduleType)
	}
	return
}

func (app *DefaultApp) GetSettings() conf.Config {
	return app.settings
}
func (app *DefaultApp) GetProcessID() string {
	return app.processId
}
func (app *DefaultApp) RpcInvoke(module module.RPCModule, moduleType string, _func string, params ...interface{}) (result interface{}, err string) {
	server, e := app.GetRouteServer(moduleType, module.GetServerId())
	if e != nil {
		err = e.Error()
		return
	}
	return server.Call(_func, params...)
}

func (app *DefaultApp) RpcInvokeNR(module module.RPCModule, moduleType string, _func string, params ...interface{}) (err error) {
	server, err := app.GetRouteServer(moduleType, module.GetServerId())
	if err != nil {
		return
	}
	return server.CallNR(_func, params...)
}

func (app *DefaultApp) RpcInvokeArgs(module module.RPCModule, moduleType string, _func string, ArgsType []string, args [][]byte) (result interface{}, err string) {
	server, e := app.GetRouteServer(moduleType, module.GetServerId())
	if e != nil {
		err = e.Error()
		return
	}
	return server.CallArgs(_func, ArgsType, args)
}

func (app *DefaultApp) RpcInvokeNRArgs(module module.RPCModule, moduleType string, _func string, ArgsType []string, args [][]byte) (err error) {
	server, err := app.GetRouteServer(moduleType, module.GetServerId())
	if err != nil {
		return
	}
	return server.CallNRArgs(_func, ArgsType, args)
}

func (app *DefaultApp) GetModuleInited() func(app module.App, module module.Module) {
	return app.moduleInited
}

func (app *DefaultApp) OnConfigurationLoaded(_func func(app module.App)) error {
	app.configurationLoaded = _func
	return nil
}

func (app *DefaultApp) OnModuleInited(_func func(app module.App, module module.Module)) error {
	app.moduleInited = _func
	return nil
}

func (app *DefaultApp) OnStartup(_func func(app module.App)) error {
	app.startup = _func
	return nil
}

func (app *DefaultApp) SetProtocolMarshal(protocolMarshal func(Trace string, Result interface{}, Error string) (module.ProtocolMarshal, string)) error {
	app.protocolMarshal = protocolMarshal
	return nil
}

func (app *DefaultApp) ProtocolMarshal(Trace string, Result interface{}, Error string) (module.ProtocolMarshal, string) {
	if app.protocolMarshal != nil {
		return app.protocolMarshal(Trace, Result, Error)
	}
	r := &resultInfo{
		Trace:  Trace,
		Error:  Error,
		Result: Result,
	}
	b, err := json.Marshal(r)
	if err == nil {
		return app.NewProtocolMarshal(b), ""
	} else {
		return nil, err.Error()
	}
}

func (app *DefaultApp) NewProtocolMarshal(data []byte) module.ProtocolMarshal {
	return &protocolMarshalImp{
		data: data,
	}
}
