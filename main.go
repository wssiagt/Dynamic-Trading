package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/adshao/go-binance/v2"
	"github.com/adshao/go-binance/v2/delivery"
	"github.com/adshao/go-binance/v2/futures"
	"github.com/tidwall/gjson"
	"gopkg.in/yaml.v2"
	"io/ioutil"
	"log"
	"math"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"
)

func ErrHandler(err error) {
	log.Println(err)
}

type FutureOrderTradeUpdateEvent struct {
	Symbol           string `json:"s"`
	OrderID          int64  `json:"i"`
	ClientOrderID    string `json:"c"`
	Price            string `json:"p"`
	ReduceOnly       bool   `json:"R"`
	OrigQuantity     string `json:"q"`
	ExecutedQuantity string `json:"z"`
	TimeInForce      string `json:"f"`
	Type             string `json:"o"`
	Side             string `json:"S"`
	StopPrice        string `json:"sp"`
	WorkingType      string `json:"wt"`
	AvgPrice         string `json:"ap"`
	OrigType         string `json:"ot"`
	OrderStatus      string `json:"x"`
	LastFillQty      string `json:"l"`
	LastFillPrice    string `json:"L"`
	UpdateTime       time.Time
}

type Strategy struct {
	ApiKey             string `yaml:"ApiKey"`
	SecretKey          string `yaml:"SecretKey"`
	FutureClient       *futures.Client
	Client             *binance.Client
	DeliveryClient     *delivery.Client
	LastOrderCheckTime time.Time
	LastOrderTradeTime time.Time
	LastPriceEventTime time.Time

	//
	LastIndexPrice  float64 //最近的指数价格
	LastMarkPrice   float64 //最近的标记价格
	LastTradedPrice float64 //最后的交易价格

	PricePrecision    int     //price精度
	QuantityPrecision int     //amount精度
	RiskPct           float64 `yaml:"RiskPct"` //#风险控制，当前成交价据挂单0.001则重新挂该挂单

	IsPriceProtest    bool                           //当前是否偏离过多，开启价格保护
	LongWinOrder      *futures.CreateOrderResponse   //当成交了lshort方向的单，则用这个单去盈利
	ShortWinOrder     *futures.CreateOrderResponse   //当成交了long方向的单，则用这个单去盈利
	OldLongWinOrders  []*futures.CreateOrderResponse //记录调整盈利单失败时候的盈利单
	OldShortWinOrders []*futures.CreateOrderResponse //记录调整盈利单失败时候的盈利单
	LongOrderList     []*futures.CreateOrderResponse
	ShortOrderList    []*futures.CreateOrderResponse

	//策略参数
	OrderWaveCancel    float64 `yaml:"OrderWaveCancel"`    //挡位波动撤单
	DeviatePriceCancel float64 `yaml:"DeviatePriceCancel"` //# 偏离标记价格撤单 = 0.02
	PriceEventInterval int     `yaml:"PriceEventInterval"` //价格事件处理将额额
	IfStoploss         bool    `yaml:"IfStoploss"`         //# 是否开启止损：false

	HoldingTime int `yaml:"HoldingTime"` // 成交后仓位持仓 = 130s

	StoplossPct float64 `yaml:"StoplossPct"` // 止损百分比 = 0.001

	Symbol       string  `yaml:"Symbol"`
	Period       int     `yaml:"Period"`       //检查订单间隔
	MaxOrderSize int     `yaml:"MaxOrderSize"` //一侧挂单最多订单数
	TargetProfit float64 `yaml:"TargetProfit"` //挂单止盈利润，利润0.0008-0.0012，默认0.001
	MaxPosition  float64 `yaml:"MaxPosition"`  //。。最大持仓，，超出后暂停该方向挂单

	LongQty   float64 `yaml:"LongQty"`   //看多下单量
	LongStart float64 `yaml:"LongStart"` //订单范围开始
	LongEnd   float64 `yaml:"LongEnd"`   //订单范围结束

	ShortQty   float64 `yaml:"ShortQty"`   //看空下单量
	ShortStart float64 `yaml:"ShortStart"` //订单范围开始
	ShortEnd   float64 `yaml:"ShortEnd"`   //订单范围结束

}

func NewStrategy() *Strategy {
	s := &Strategy{}
	s.LongOrderList = []*futures.CreateOrderResponse{}
	s.ShortOrderList = []*futures.CreateOrderResponse{}
	return s
}

type EventStream struct {
	EventType string
	Event     interface{}
}

// 策略初始化，读取相应参数
func (s *Strategy) Init() {
	yamlFile, err := ioutil.ReadFile("./config.yaml")
	if err != nil {
		log.Printf("yamlFile.Get err   #%v ", err)
	}

	//strategyConfig := &Strategy{}
	err = yaml.Unmarshal(yamlFile, s)
	if err != nil {
		log.Fatal(err)
	}

	//初始化client
	s.Client = binance.NewClient(s.ApiKey, s.SecretKey)
	s.FutureClient = binance.NewFuturesClient(s.ApiKey, s.SecretKey)    // USDT-M Futures
	s.DeliveryClient = binance.NewDeliveryClient(s.ApiKey, s.SecretKey) // Coin-M Futures
	s.LastOrderCheckTime = time.Now().Add(-1 * time.Hour)

	exchangeInfo, err := s.FutureClient.NewExchangeInfoService().Do(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	for _, symbolInfo := range exchangeInfo.Symbols {
		if symbolInfo.Symbol == s.Symbol {
			s.PricePrecision = symbolInfo.PricePrecision
			s.QuantityPrecision = symbolInfo.QuantityPrecision
		}
	}

	//取消所有挂单
	log.Println("初始化取消所有挂单")
	err = s.FutureClient.NewCancelAllOpenOrdersService().Symbol(s.Symbol).
		Do(context.Background())
	if err != nil {
		log.Fatalln(err)
	}

	//平掉所有仓位
	s.CancelAll()
}
func (s *Strategy) CancelAll() {
	res, err := s.FutureClient.NewGetAccountService().Do(context.Background())
	if err != nil {
		log.Fatalln(err)
	}
	for _, pos := range res.Positions {
		positionAmt, _ := strconv.ParseFloat(pos.PositionAmt, 64)
		if positionAmt > 0 {
			s.FutureClient.NewCreateOrderService().Symbol(s.Symbol).
				Side(futures.SideTypeBuy).Type(futures.OrderTypeMarket).ReduceOnly(true).Quantity(fmt.Sprintf("%."+strconv.Itoa(s.QuantityPrecision)+"f", positionAmt)).Do(context.Background())

			s.FutureClient.NewCreateOrderService().Symbol(s.Symbol).
				Side(futures.SideTypeSell).Type(futures.OrderTypeMarket).ReduceOnly(true).Quantity(fmt.Sprintf("%."+strconv.Itoa(s.QuantityPrecision)+"f", positionAmt)).Do(context.Background())

		}
	}
}
func (s *Strategy) Check() {

}

func (s *Strategy) OpenLongWaveOrder() {
	for i := 0; i < s.MaxOrderSize; i++ {
		// 买单间隔
		buyPriceInterval := (s.LongEnd - s.LongStart) / float64(s.MaxOrderSize)
		// 下买单
		for {
			order, err := s.FutureClient.NewCreateOrderService().Symbol(s.Symbol).
				Side(futures.SideTypeBuy).Type(futures.OrderTypeLimit).
				TimeInForce(futures.TimeInForceTypeGTC).Quantity(fmt.Sprintf("%."+strconv.Itoa(s.QuantityPrecision)+"f", s.LongQty)).
				Price(fmt.Sprintf("%."+strconv.Itoa(s.PricePrecision)+"f", s.LastTradedPrice*(1-s.LongEnd))).Do(context.Background())
																								// (1-(s.LongStart+Size*(s.LongEnd-s.LongStart)/Size)=(1-s.LongEnd)
			if err != nil {
				log.Println(err)
				continue
			} else {
				s.LongOrderList = append(s.LongOrderList, order)
				break
			}
		}
	}
}
func (s *Strategy) OpenShortWaveOrder() {
	for i := 0; i < s.MaxOrderSize; i++ {
		//卖单间隔
		shortPriceInterval := (s.ShortEnd - s.ShortStart) / float64(s.MaxOrderSize)

		for {
			order, err := s.FutureClient.NewCreateOrderService().Symbol(s.Symbol).
				Side(futures.SideTypeSell).Type(futures.OrderTypeLimit).
				TimeInForce(futures.TimeInForceTypeGTC).Quantity(fmt.Sprintf("%."+strconv.Itoa(s.QuantityPrecision)+"f", s.ShortQty)).
				Price(fmt.Sprintf("%."+strconv.Itoa(s.PricePrecision)+"f", s.LastTradedPrice*(1+s.ShortEnd))).Do(context.Background())
																							// (1+(s.ShortStart+Size*(s.ShortEnd-s.ShortStart)/Size)=(1+s.ShortEnd)
			if err != nil {
				log.Println(err)
				continue
			} else {
				s.ShortOrderList = append(s.ShortOrderList, order)
				break
			}
		}
	}
}
func (s *Strategy) GetSidePosition(side futures.SideType) float64 {

	if side == futures.SideTypeBuy {
		if s.LongWinOrder == nil {
			return 0
		}
		origQty, _ := strconv.ParseFloat(s.LongWinOrder.OrigQuantity, 64)
		eQty, _ := strconv.ParseFloat(s.LongWinOrder.ExecutedQuantity, 64)
		leftQty := origQty - eQty
		return leftQty
	} else if side == futures.SideTypeSell {
		if s.ShortWinOrder == nil {
			return 0
		}
		origQty, _ := strconv.ParseFloat(s.ShortWinOrder.OrigQuantity, 64)
		eQty, _ := strconv.ParseFloat(s.ShortWinOrder.ExecutedQuantity, 64)
		leftQty := origQty - eQty
		return leftQty
	}
	return 0
}

// 处理信息推送,这里有缓冲时间 (风控、止损、价格保护、价格调整)
func (s *Strategy) EventHandler(event *EventStream) {

	//看是否触发止损
	if s.IfStoploss && (s.LongWinOrder != nil || s.ShortWinOrder != nil) {
		if time.Now().After(s.LastOrderTradeTime.Add(time.Duration(s.HoldingTime) * time.Second)) {

			if s.LongWinOrder != nil || s.ShortWinOrder != nil {
				log.Println("止损", s.HoldingTime, "秒未获利和仓位改变，平掉所有仓位和挂单")
				//取消所有挂单
				s.FutureClient.NewCancelAllOpenOrdersService().Symbol(s.Symbol).Do(context.Background())

				if s.LongWinOrder != nil {
					origQty, _ := strconv.ParseFloat(s.LongWinOrder.OrigQuantity, 64)
					_, err := s.FutureClient.NewCreateOrderService().Symbol(s.Symbol).
						Side(futures.SideTypeBuy).Type(futures.OrderTypeMarket).ReduceOnly(true).Quantity(fmt.Sprintf("%."+strconv.Itoa(s.QuantityPrecision)+"f", 2*origQty)).Do(context.Background())
					if err != nil {
						log.Println(err)
					}
					s.LongWinOrder = nil
				}
				if s.ShortWinOrder != nil {
					origQty, _ := strconv.ParseFloat(s.ShortWinOrder.OrigQuantity, 64)
					_, err := s.FutureClient.NewCreateOrderService().Symbol(s.Symbol).
						Side(futures.SideTypeSell).Type(futures.OrderTypeMarket).ReduceOnly(true).Quantity(fmt.Sprintf("%."+strconv.Itoa(s.QuantityPrecision)+"f", 2*origQty)).Do(context.Background())
					if err != nil {
						log.Println(err)
					}
					s.ShortWinOrder = nil
				}
			}
			s.LastOrderTradeTime = time.Now()
			//time.Sleep(time.Second)
			return
		}
	}

	if event.EventType == "MarkPriceEvent" {
		markPriceEvent := event.Event.(*futures.WsMarkPriceEvent)
		lastMarkPrice, _ := strconv.ParseFloat(markPriceEvent.MarkPrice, 64)
		lastIndexPrice, _ := strconv.ParseFloat(markPriceEvent.IndexPrice, 64)
		s.LastMarkPrice = lastMarkPrice
		s.LastIndexPrice = lastIndexPrice
	}

	if event.EventType == "AggTradeEvent" {
		aggTradeEvent := event.Event.(*futures.WsAggTradeEvent)
		lastTradedPrice, _ := strconv.ParseFloat(aggTradeEvent.Price, 64)
		s.LastTradedPrice = lastTradedPrice

		//风控调整挂单
		if len(s.LongOrderList) > 0 && !s.IsPriceProtest {
			longOrderPrce, _ := strconv.ParseFloat(s.LongOrderList[0].Price, 64)
			// 如果当前价格距第一档挂单只有千分之一则取消后挂最后
			if math.Abs(longOrderPrce-s.LastTradedPrice)/longOrderPrce < s.RiskPct {
				log.Println("风控尝试调整Long挂单")
				_, err := s.FutureClient.NewCancelOrderService().Symbol(s.Symbol).OrderID(s.LongOrderList[0].OrderID).
					Do(context.Background())
				if err != nil {
					log.Println("风控调整失败，可能已经成交")

				} else {
					// 下买单
					buyPriceInterval := (s.LongEnd - s.LongStart) / float64(s.MaxOrderSize)
					for {
						order, err := s.FutureClient.NewCreateOrderService().Symbol(s.Symbol).
							Side(futures.SideTypeBuy).Type(futures.OrderTypeLimit).
							TimeInForce(futures.TimeInForceTypeGTC).Quantity(fmt.Sprintf("%."+strconv.Itoa(s.QuantityPrecision)+"f", s.LongQty)).
							Price(fmt.Sprintf("%."+strconv.Itoa(s.PricePrecision)+"f", s.LastTradedPrice*(1-s.LongEnd))).Do(context.Background())
																										// (1-(s.LongStart+Size*(s.LongEnd-s.LongStart)/Size)=(1-s.LongEnd)
						if err != nil {
							time.Sleep(100 * time.Millisecond)
							log.Println(err)
							continue
						} else {
							s.LongOrderList = append(s.LongOrderList[1:s.MaxOrderSize], order)
							break
						}
					}
				}
			}
		}
		//风控调整挂单
		if len(s.ShortOrderList) > 0 && !s.IsPriceProtest {
			shortOrderPrce, _ := strconv.ParseFloat(s.ShortOrderList[0].Price, 64)
			if math.Abs(shortOrderPrce-s.LastTradedPrice)/shortOrderPrce < s.RiskPct {
				log.Println("风控尝试调整Short挂单")
				_, err := s.FutureClient.NewCancelOrderService().Symbol(s.Symbol).OrderID(s.ShortOrderList[0].OrderID).
					Do(context.Background())
				if err != nil {
					log.Println("风控调整失败，可能已经成交")
				} else {
					// 下卖单
					shortPriceInterval := (s.ShortEnd - s.ShortStart) / float64(s.MaxOrderSize)
					for {
						order, err := s.FutureClient.NewCreateOrderService().Symbol(s.Symbol).
							Side(futures.SideTypeSell).Type(futures.OrderTypeLimit).
							TimeInForce(futures.TimeInForceTypeGTC).Quantity(fmt.Sprintf("%."+strconv.Itoa(s.QuantityPrecision)+"f", s.ShortQty)).
							Price(fmt.Sprintf("%."+strconv.Itoa(s.PricePrecision)+"f", s.LastTradedPrice*(1+s.ShortEnd))).Do(context.Background())
																					                   // (1+(s.ShortStart+Size*(s.ShortEnd-s.ShortStart)/Size)=(1+s.ShortEnd)			
						if err != nil {
							time.Sleep(100 * time.Millisecond)
							log.Println(err)
							continue
						} else {
							s.ShortOrderList = append(s.ShortOrderList[1:s.MaxOrderSize], order)
							break
						}
					}
				}

			}
		}
	}

	// 如果偏离过多启动价格保护
	if s.LastTradedPrice > 0 && s.LastIndexPrice > 0 && math.Abs(s.LastTradedPrice-s.LastIndexPrice)/s.LastIndexPrice > s.DeviatePriceCancel {
		if !s.IsPriceProtest {
			log.Println("最近成交价偏离现货指数价格过多，开启价格保护")
		}
		s.IsPriceProtest = true
		openOrders, err := s.FutureClient.NewListOpenOrdersService().Symbol(s.Symbol).
			Do(context.Background())
		if err != nil {
			log.Println(err)
			return
		}
		for _, o := range openOrders {
			if (s.LongWinOrder != nil && s.LongWinOrder.OrderID == o.OrderID) || (s.ShortWinOrder != nil && s.ShortWinOrder.OrderID == o.OrderID) {
				continue
			} else {
				_, err = s.FutureClient.NewCancelOrderService().Symbol(s.Symbol).OrderID(o.OrderID).
					Do(context.Background())
				if err != nil {
					log.Println(err)
				}
			}
		}

		s.ShortOrderList = []*futures.CreateOrderResponse{}
		s.LongOrderList = []*futures.CreateOrderResponse{}

	} else if s.IsPriceProtest == true && s.LastTradedPrice > 0 && s.LastIndexPrice > 0 && math.Abs(s.LastTradedPrice-s.LastIndexPrice)/s.LastIndexPrice <= s.DeviatePriceCancel {
		log.Println("价格恢复正常，关闭价格保护")
		s.IsPriceProtest = false
	}

	//只有AggTradeEvent和MarkPriceEvent事件才检查挂单数量，看是否要减少或者增多
	if event.EventType == "AggTradeEvent" || event.EventType == "MarkPriceEvent" {
		openOrders, err := s.FutureClient.NewListOpenOrdersService().Symbol(s.Symbol).
			Do(context.Background())
		if err != nil {
			log.Println(err)
			return
		}

		//如果订单过多
		if s.GetSidePosition(futures.SideTypeBuy) >= s.MaxPosition || s.GetSidePosition(futures.SideTypeSell) >= s.MaxPosition {
			if s.GetSidePosition(futures.SideTypeBuy) >= s.MaxPosition && len(s.ShortOrderList) != 0 {
				for _, o := range openOrders {
					if o.Side == futures.SideTypeSell && (s.ShortWinOrder == nil || (s.ShortWinOrder != nil && s.ShortWinOrder.OrderID != o.OrderID)) { //	取消挂单
						_, err = s.FutureClient.NewCancelOrderService().Symbol(s.Symbol).OrderID(o.OrderID).
							Do(context.Background())
						if err != nil {
							log.Println(err)
						}
					}
				}
				s.ShortOrderList = []*futures.CreateOrderResponse{}
			}

			if s.GetSidePosition(futures.SideTypeSell) >= s.MaxPosition && len(s.LongOrderList) != 0 {
				for _, o := range openOrders {
					if o.Side == futures.SideTypeBuy && (s.LongWinOrder == nil || (s.LongWinOrder != nil && s.LongWinOrder.OrderID != o.OrderID)) { //	取消挂单
						_, err = s.FutureClient.NewCancelOrderService().Symbol(s.Symbol).OrderID(o.OrderID).
							Do(context.Background())
						if err != nil {
							log.Println(err)
						}
					}
				}
				s.LongOrderList = []*futures.CreateOrderResponse{}
			}

		} else if !s.IsPriceProtest && (len(openOrders) < 2*s.MaxOrderSize-1 || len(s.LongOrderList) == 0 || len(s.ShortOrderList) == 0) {
			//如果刚订单数过少
			//如果还没有记录交易价格则退出一次循环
			if s.LastTradedPrice == 0 {
				return
			}

			log.Println("当前订单数", len(openOrders), "初始化挂单")
			for _, o := range openOrders {
				if s.ShortWinOrder != nil && s.ShortWinOrder.OrderID == o.OrderID {
					continue
				}
				if s.LongWinOrder != nil && s.LongWinOrder.OrderID == o.OrderID {
					continue
				}
				// 重置除了盈利单的所有订单
				if o.Symbol == s.Symbol {
					s.FutureClient.NewCancelOrderService().Symbol(s.Symbol).OrderID(o.OrderID).
						Do(context.Background())
				}

			}
			s.LongOrderList = []*futures.CreateOrderResponse{}
			s.ShortOrderList = []*futures.CreateOrderResponse{}

			s.OpenLongWaveOrder()
			s.OpenShortWaveOrder()
		}
	}
	if !s.IsPriceProtest && (event.EventType == "AggTradeEvent" || event.EventType == "MarkPriceEvent") && time.Now().After(s.LastOrderCheckTime.Add(time.Duration(s.Period)*time.Second)) {
		//	如果已经有订单了，检查是否有要调整的订单，这里只调整价格不调整仓位
		//log.Println("检查挂单位置")
		s.LastOrderCheckTime = time.Now()
		// 调整Long挂单
		buyPriceInterval := (s.LongEnd - s.LongStart) / float64(s.MaxOrderSize)
		for i := 0; i < len(s.LongOrderList); i++ {
			price, _ := strconv.ParseFloat(s.LongOrderList[i].Price, 64)
			orderWavePrice := s.LastTradedPrice * (1 - (s.LongStart + float64(i+1)*buyPriceInterval))

			if math.Abs((orderWavePrice-price))/price > s.OrderWaveCancel {
				//log.Println("调整第", i+1, "个买单", "原价：", price, ",新价格:", orderWavePrice)
				//	取消挂单
				_, err := s.FutureClient.NewCancelOrderService().Symbol(s.Symbol).OrderID(s.LongOrderList[i].OrderID).
					Do(context.Background())
				if err != nil {
					log.Println(err)
					//time.Sleep(100 * time.Millisecond)
				} else {
					//	挂新的订单，计算需要挂单的量
					eQty, _ := strconv.ParseFloat(s.LongOrderList[i].ExecutedQuantity, 64)
					orderQty := s.LongQty - eQty
					if orderQty >= math.Pow(0.1, float64(s.QuantityPrecision)) {
						for {
							order, err := s.FutureClient.NewCreateOrderService().Symbol(s.Symbol).
								Side(futures.SideTypeBuy).Type(futures.OrderTypeLimit).
								TimeInForce(futures.TimeInForceTypeGTC).Quantity(fmt.Sprintf("%."+strconv.Itoa(s.QuantityPrecision)+"f", orderQty)).
								Price(fmt.Sprintf("%."+strconv.Itoa(s.PricePrecision)+"f", orderWavePrice)).Do(context.Background())
							if err != nil {
								log.Println(err)
								continue
							} else {
								s.LongOrderList[i] = order
								break
							}
						}
					}
				}

			}
		}

		//调整Short单
		shortPriceInterval := (s.ShortEnd - s.ShortStart) / float64(s.MaxOrderSize)
		for i := 0; i < len(s.ShortOrderList); i++ {
			price, _ := strconv.ParseFloat(s.ShortOrderList[i].Price, 64)
			orderWavePrice := s.LastTradedPrice * (1 + (s.ShortStart + float64(i+1)*shortPriceInterval))

			if math.Abs((orderWavePrice-price))/price > s.OrderWaveCancel {
				//log.Println("调整第", i+1, "个卖单", "原价：", price, ",新价格:", orderWavePrice)
				//	取消挂单
				_, err := s.FutureClient.NewCancelOrderService().Symbol(s.Symbol).OrderID(s.ShortOrderList[i].OrderID).
					Do(context.Background())
				if err != nil {
					log.Println(err)
				} else {
					//	挂新的订单，计算需要挂单的量
					eQty, _ := strconv.ParseFloat(s.ShortOrderList[i].ExecutedQuantity, 64)
					orderQty := s.ShortQty - eQty
					if orderQty >= math.Pow(0.1, float64(s.QuantityPrecision)) {
						for {
							order, err := s.FutureClient.NewCreateOrderService().Symbol(s.Symbol).
								Side(futures.SideTypeSell).Type(futures.OrderTypeLimit).
								TimeInForce(futures.TimeInForceTypeGTC).Quantity(fmt.Sprintf("%."+strconv.Itoa(s.QuantityPrecision)+"f", orderQty)).
								Price(fmt.Sprintf("%."+strconv.Itoa(s.PricePrecision)+"f", orderWavePrice)).Do(context.Background())
							if err != nil {
								log.Println(err)
								continue
							} else {
								s.ShortOrderList[i] = order
								break
							}
						}
					}
				}
			}
		}
	}

	if event.EventType == "OrderTradeUpdateEvent" {
		s.OrderTradeEventHandler(event)
	}
}

// 处理订单成交的函数
func (s *Strategy) OrderTradeEventHandler(event *EventStream) {

	orderEvent := event.Event.(*FutureOrderTradeUpdateEvent)

	//如果有止损，过滤掉止损单
	if s.IfStoploss && orderEvent.OrigType == "MARKET" {
		return
	}

	//log.Println(orderEvent)
	//	判断是买单成交了还是卖单成交了，是否在挂单中
	if orderEvent.Side == "BUY" {
		log.Println("有Long订单成交，成交量为:", orderEvent.LastFillQty, ",成交价为:", orderEvent.LastFillPrice)
		//如果是之前调整失败的盈利单，则跳过

		if len(s.OldLongWinOrders) > 0 {
			for _, order := range s.OldLongWinOrders {
				if order.OrderID == orderEvent.OrderID {
					log.Println("这是之前的盈利单，跳过")
					return
				}
			}
		}
		//判断是不是盈利单，注意如果是盈利单而且全部成交，则取消所有挂单重新进入循环
		if s.LongWinOrder != nil && s.LongWinOrder.OrderID == orderEvent.OrderID {
			log.Println("Long方向盈利单成交")
			//如果是全部成交，则重置盈利单，取消所有挂单
			if orderEvent.OrderStatus == "FILLED" {
				s.LongWinOrder = nil
				// 取消非short盈利挂单
				if s.ShortWinOrder == nil {
					err := s.FutureClient.NewCancelAllOpenOrdersService().Symbol(s.Symbol).
						Do(context.Background())
					if err != nil {
						log.Println(err)
					}
					s.ShortOrderList = []*futures.CreateOrderResponse{}
					s.LongOrderList = []*futures.CreateOrderResponse{}
				} else {
					log.Println("当前有short方向盈利单，只取消short方向挂单")
					// 检查挂单的
					openOrders, err := s.FutureClient.NewListOpenOrdersService().Symbol(s.Symbol).
						Do(context.Background())
					if err != nil {
						log.Println(err)
					}
					for _, o := range openOrders {
						//	取消挂单
						if o.OrderID != s.ShortWinOrder.OrderID && o.Side == "SELL" {
							_, err = s.FutureClient.NewCancelOrderService().Symbol(s.Symbol).OrderID(o.OrderID).
								Do(context.Background())
							if err != nil {
								log.Println(err)
							}
						}
					}
					s.ShortOrderList = []*futures.CreateOrderResponse{}
					if !s.IsPriceProtest {
						s.OpenShortWaveOrder()
					}

				}
			} else if orderEvent.OrderStatus == "PARTIALLY_FILLED" {
				//	如果是部分成交，更新止盈单信息
				s.LongWinOrder.ExecutedQuantity = orderEvent.ExecutedQuantity
			}
		} else if s.ShortWinOrder == nil || s.ShortWinOrder.OrderID != orderEvent.OrderID {
			//if s.LongOrderList[i].OrderID == orderEvent.OrderID {
			if s.ShortWinOrder == nil {

				lastFillPrice, _ := strconv.ParseFloat(orderEvent.LastFillPrice, 64)
				lastFillQty, _ := strconv.ParseFloat(orderEvent.LastFillQty, 64)
				newPrice := lastFillPrice * (1 + s.TargetProfit)
				log.Println("新Short方向盈利单,原始订单价：", orderEvent.Price, "，新订单盈利价:", newPrice)
				for {
					order, err := s.FutureClient.NewCreateOrderService().Symbol(s.Symbol).
						Side(futures.SideTypeSell).Type(futures.OrderTypeLimit).
						TimeInForce(futures.TimeInForceTypeGTC).Quantity(fmt.Sprintf("%."+strconv.Itoa(s.QuantityPrecision)+"f", lastFillQty)).
						Price(fmt.Sprintf("%."+strconv.Itoa(s.PricePrecision)+"f", newPrice)).Do(context.Background())
					if err != nil {
						log.Println(err)
						continue
					} else {
						s.ShortWinOrder = order
						break
					}
				}
			} else {
				//	如果已经有止盈单，则调整，先取消后调整
				log.Println("调整Short方向盈利单")
				//	取消挂单
				_, err := s.FutureClient.NewCancelOrderService().Symbol(s.Symbol).OrderID(s.ShortWinOrder.OrderID).
					Do(context.Background())
				if err != nil {
					log.Println(err)
					lastFillPrice, _ := strconv.ParseFloat(orderEvent.LastFillPrice, 64)
					lastFillQty, _ := strconv.ParseFloat(orderEvent.LastFillQty, 64)
					newPrice := lastFillPrice * (1 + s.TargetProfit)
					log.Println("调整失败，可能已经成交，下盈利单。原始订单价：", orderEvent.Price, "，新订单盈利价:", newPrice)

					if len(s.OldShortWinOrders)<10{
						s.OldShortWinOrders = append(s.OldShortWinOrders,s.ShortWinOrder)
					}else{
						s.OldShortWinOrders = append(s.OldShortWinOrders[1:len(s.OldShortWinOrders)],s.ShortWinOrder)
					}
					s.ShortWinOrder=nil
					for {
						if lastFillQty*lastFillPrice < 1.1 {
							log.Println("这次成交过小，重置所有订单和持仓")
							s.CancelAll()
							break
						}
						order, err := s.FutureClient.NewCreateOrderService().Symbol(s.Symbol).
							Side(futures.SideTypeSell).Type(futures.OrderTypeLimit).
							TimeInForce(futures.TimeInForceTypeGTC).Quantity(fmt.Sprintf("%."+strconv.Itoa(s.QuantityPrecision)+"f", lastFillQty)).
							Price(fmt.Sprintf("%."+strconv.Itoa(s.PricePrecision)+"f", newPrice)).Do(context.Background())
						if err != nil {
							log.Println(err)
							continue
						} else {
							s.ShortWinOrder = order
							break
						}
					}
				} else {
					//下新的止盈单
					nowOrderPrice, _ := strconv.ParseFloat(s.ShortWinOrder.Price, 64)
					nowOrderOrigQty, _ := strconv.ParseFloat(s.ShortWinOrder.OrigQuantity, 64)
					nowOrderEQty, _ := strconv.ParseFloat(s.ShortWinOrder.ExecutedQuantity, 64)
					nowOrderLeftQty := nowOrderOrigQty - nowOrderEQty
					orderLastFillQty, _ := strconv.ParseFloat(orderEvent.LastFillQty, 64)
					orderLastFillPrice, _ := strconv.ParseFloat(orderEvent.LastFillPrice, 64)
					newOrderPrice := nowOrderPrice*(nowOrderLeftQty/(nowOrderLeftQty+orderLastFillQty)) + orderLastFillPrice*(1+s.TargetProfit)*(orderLastFillQty/(nowOrderLeftQty+orderLastFillQty))
					newOrderQty := orderLastFillQty + nowOrderLeftQty
					for {
						order, err := s.FutureClient.NewCreateOrderService().Symbol(s.Symbol).
							Side(futures.SideTypeSell).Type(futures.OrderTypeLimit).
							TimeInForce(futures.TimeInForceTypeGTC).Quantity(fmt.Sprintf("%."+strconv.Itoa(s.QuantityPrecision)+"f", newOrderQty)).
							Price(fmt.Sprintf("%."+strconv.Itoa(s.PricePrecision)+"f", newOrderPrice)).Do(context.Background())
						if err != nil {
							time.Sleep(100 * time.Millisecond)
							log.Println(err)
							continue
						} else {
							s.ShortWinOrder = order
							break
						}
					}
				}

			}

			//如果是完全成交在最后挂一个新的单
			if orderEvent.OrderStatus == "FILLED" && s.GetSidePosition(futures.SideTypeSell) < s.MaxPosition {
				// 下买单
				buyPriceInterval := (s.LongEnd - s.LongStart) / float64(s.MaxOrderSize)
				for {
					order, err := s.FutureClient.NewCreateOrderService().Symbol(s.Symbol).
						Side(futures.SideTypeBuy).Type(futures.OrderTypeLimit).
						TimeInForce(futures.TimeInForceTypeGTC).Quantity(fmt.Sprintf("%."+strconv.Itoa(s.QuantityPrecision)+"f", s.LongQty)).
						Price(fmt.Sprintf("%."+strconv.Itoa(s.PricePrecision)+"f", s.LastTradedPrice*(1-(s.LongStart+float64(s.MaxOrderSize+1)*buyPriceInterval)))).Do(context.Background())
					if err != nil {
						log.Println(err)
						continue
					} else {
						if len(s.LongOrderList) >= 2 {
							s.LongOrderList = append(s.LongOrderList[1:s.MaxOrderSize], order)
						} else {
							s.LongOrderList = []*futures.CreateOrderResponse{}
							s.LongOrderList = append(s.LongOrderList, order)
						}
						break
					}
				}

			}

		}

	} else {
		// 假如是short单成交
		log.Println("有Short订单成交,成交量为:", orderEvent.LastFillQty, ",成交价为:", orderEvent.LastFillPrice)
		//如果是之前调整失败的盈利单，则跳过
		if len(s.OldShortWinOrders) > 0 {
			for _, order := range s.OldShortWinOrders {
				if order.OrderID == orderEvent.OrderID {
					log.Println("这是之前的盈利单，跳过")
					return
				}
			}
		}
		//判断是不是盈利单，注意如果是盈利单而且全部成交，则取消所有挂单重新进入循环
		if s.ShortWinOrder != nil && s.ShortWinOrder.OrderID == orderEvent.OrderID {
			log.Println("Short方向盈利单成交")
			//如果是全部成交，则重置盈利单，取消所有挂单
			if orderEvent.OrderStatus == "FILLED" {
				s.ShortWinOrder = nil
				// 取消非long盈利的buy挂单
				if s.LongWinOrder == nil {
					err := s.FutureClient.NewCancelAllOpenOrdersService().Symbol(s.Symbol).
						Do(context.Background())
					if err != nil {
						log.Println(err)
					}
					s.ShortOrderList = []*futures.CreateOrderResponse{}
					s.LongOrderList = []*futures.CreateOrderResponse{}
				} else {
					log.Println("当前有Long方向盈利单，只取消Long方向挂单")
					// 检查挂单的
					openOrders, err := s.FutureClient.NewListOpenOrdersService().Symbol(s.Symbol).
						Do(context.Background())
					if err != nil {
						log.Println(err)
					}
					for _, o := range openOrders {
						//	取消挂单
						if o.OrderID != s.LongWinOrder.OrderID && o.Side == "BUY" {
							_, err = s.FutureClient.NewCancelOrderService().Symbol(s.Symbol).OrderID(o.OrderID).
								Do(context.Background())
							if err != nil {
								log.Println(err)
							}
						}
					}
					s.LongOrderList = []*futures.CreateOrderResponse{}
					if !s.IsPriceProtest {
						s.OpenLongWaveOrder()
					}
				}

			} else if orderEvent.OrderStatus == "PARTIALLY_FILLED" {
				//	如果是部分成交，更新止盈单信息
				s.ShortWinOrder.ExecutedQuantity = orderEvent.ExecutedQuantity
			}
		} else if s.LongWinOrder == nil || s.LongWinOrder.OrderID != orderEvent.OrderID {
			if s.LongWinOrder == nil {

				lastFillPrice, _ := strconv.ParseFloat(orderEvent.LastFillPrice, 64)
				lastFillQty, _ := strconv.ParseFloat(orderEvent.LastFillQty, 64)
				newPrice := lastFillPrice * (1 - s.TargetProfit)
				log.Println("新Long方向盈利单,原始订单价：", orderEvent.Price, "，新订单盈利价:", newPrice)
				for {
					order, err := s.FutureClient.NewCreateOrderService().Symbol(s.Symbol).
						Side(futures.SideTypeBuy).Type(futures.OrderTypeLimit).
						TimeInForce(futures.TimeInForceTypeGTC).Quantity(fmt.Sprintf("%."+strconv.Itoa(s.QuantityPrecision)+"f", lastFillQty)).
						Price(fmt.Sprintf("%."+strconv.Itoa(s.PricePrecision)+"f", newPrice)).Do(context.Background())
					if err != nil {
						log.Println(err)
						continue
					} else {
						s.LongWinOrder = order
						break
					}
				}
			} else {
				//	如果已经有止盈单，则调整，先取消后调整
				log.Println("调整Long方向盈利单")

				//	取消挂单
				//for {
				_, err := s.FutureClient.NewCancelOrderService().Symbol(s.Symbol).OrderID(s.LongWinOrder.OrderID).
					Do(context.Background())
				if err != nil {
					log.Println(err)
					lastFillPrice, _ := strconv.ParseFloat(orderEvent.LastFillPrice, 64)
					lastFillQty, _ := strconv.ParseFloat(orderEvent.LastFillQty, 64)
					newPrice := lastFillPrice * (1 - s.TargetProfit)

					log.Println("调整失败，可能已经成交，下盈利单。原始订单价：", orderEvent.Price, "，新订单盈利价:", newPrice)
					if len(s.OldLongWinOrders)<10{
						s.OldLongWinOrders = append(s.OldLongWinOrders,s.LongWinOrder)
					}else{
						s.OldLongWinOrders = append(s.OldLongWinOrders[1:len(s.OldLongWinOrders)],s.LongWinOrder)
					}
					s.LongWinOrder=nil
					for {
						if lastFillQty*lastFillPrice < 1.1 {
							log.Println("这次成交过小，重置所有订单和持仓")
							s.CancelAll()
							break
						}
						order, err := s.FutureClient.NewCreateOrderService().Symbol(s.Symbol).
							Side(futures.SideTypeBuy).Type(futures.OrderTypeLimit).
							TimeInForce(futures.TimeInForceTypeGTC).Quantity(fmt.Sprintf("%."+strconv.Itoa(s.QuantityPrecision)+"f", lastFillQty)).
							Price(fmt.Sprintf("%."+strconv.Itoa(s.PricePrecision)+"f", newPrice)).Do(context.Background())
						if err != nil {
							log.Println(err)
							continue
						} else {
							s.LongWinOrder = order
							break
						}
					}
				} else {
					nowOrderPrice, _ := strconv.ParseFloat(s.LongWinOrder.Price, 64)
					nowOrderOrigQty, _ := strconv.ParseFloat(s.LongWinOrder.OrigQuantity, 64)
					nowOrderEQty, _ := strconv.ParseFloat(s.LongWinOrder.ExecutedQuantity, 64)
					nowOrderLeftQty := nowOrderOrigQty - nowOrderEQty
					orderLastFillQty, _ := strconv.ParseFloat(orderEvent.LastFillQty, 64)
					orderLastFillPrice, _ := strconv.ParseFloat(orderEvent.LastFillPrice, 64)
					newOrderPrice := nowOrderPrice*(nowOrderLeftQty/(nowOrderLeftQty+orderLastFillQty)) + orderLastFillPrice*(1-s.TargetProfit)*(orderLastFillQty/(nowOrderLeftQty+orderLastFillQty))
					newOrderQty := orderLastFillQty + nowOrderLeftQty
					//下新的止盈单
					for {
						order, err := s.FutureClient.NewCreateOrderService().Symbol(s.Symbol).
							Side(futures.SideTypeBuy).Type(futures.OrderTypeLimit).
							TimeInForce(futures.TimeInForceTypeGTC).Quantity(fmt.Sprintf("%."+strconv.Itoa(s.QuantityPrecision)+"f", newOrderQty)).
							Price(fmt.Sprintf("%."+strconv.Itoa(s.PricePrecision)+"f", newOrderPrice)).Do(context.Background())
						if err != nil {
							log.Println(err)
							continue
						} else {
							s.LongWinOrder = order
							break
						}
					}
				}

			}
			// 在最后下卖单
			//如果是完全成交在最后挂一个新的单
			if orderEvent.OrderStatus == "FILLED" && s.GetSidePosition(futures.SideTypeBuy) < s.MaxPosition {
				shortPriceInterval := (s.ShortEnd - s.ShortStart) / float64(s.MaxOrderSize)
				for {
					order, err := s.FutureClient.NewCreateOrderService().Symbol(s.Symbol).
						Side(futures.SideTypeSell).Type(futures.OrderTypeLimit).
						TimeInForce(futures.TimeInForceTypeGTC).Quantity(fmt.Sprintf("%."+strconv.Itoa(s.QuantityPrecision)+"f", s.ShortQty)).
						Price(fmt.Sprintf("%."+strconv.Itoa(s.PricePrecision)+"f", s.LastTradedPrice*(1+(s.ShortStart+float64(s.MaxOrderSize+1)*shortPriceInterval)))).Do(context.Background())
					if err != nil {
						log.Println(err)
						continue
					} else {
						if len(s.ShortOrderList) >= 2 {
							s.ShortOrderList = append(s.ShortOrderList[1:s.MaxOrderSize], order)
						} else {
							s.ShortOrderList = []*futures.CreateOrderResponse{}
							s.ShortOrderList = append(s.ShortOrderList, order)
						}
						break

					}
				}
			}
		}

	}
	s.LastOrderTradeTime = time.Now()

	//仓位提醒
	if s.GetSidePosition(futures.SideTypeSell) >= s.MaxPosition {
		log.Println("Long持仓达到上限，撤销Long挂单")
	}
	if s.GetSidePosition(futures.SideTypeBuy) >= s.MaxPosition {
		log.Println("Short持仓达到上限，撤销Short挂单")
	}
}

func (s *Strategy) KillWatch() {

	c1 := make(chan os.Signal, 1)
	signal.Notify(c1, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGKILL)
	log.Println("策略收到关闭信号", <-c1)

	s.FutureClient.NewCancelAllOpenOrdersService().Symbol(s.Symbol).Do(context.Background())
	if s.LongWinOrder != nil {
		origQty, _ := strconv.ParseFloat(s.LongWinOrder.OrigQuantity, 64)
		_, err := s.FutureClient.NewCreateOrderService().Symbol(s.Symbol).
			Side(futures.SideTypeBuy).Type(futures.OrderTypeMarket).ReduceOnly(true).Quantity(fmt.Sprintf("%."+strconv.Itoa(s.QuantityPrecision)+"f", 2*origQty)).Do(context.Background())
		if err != nil {
			log.Println(err)
		}
		s.LongWinOrder = nil
	}
	if s.ShortWinOrder != nil {
		origQty, _ := strconv.ParseFloat(s.ShortWinOrder.OrigQuantity, 64)
		_, err := s.FutureClient.NewCreateOrderService().Symbol(s.Symbol).
			Side(futures.SideTypeSell).Type(futures.OrderTypeMarket).ReduceOnly(true).Quantity(fmt.Sprintf("%."+strconv.Itoa(s.QuantityPrecision)+"f", 2*origQty)).Do(context.Background())
		if err != nil {
			log.Println(err)
		}
		s.ShortWinOrder = nil
	}

}

func main() {

	strategy := NewStrategy()
	strategy.Init()

	log.SetFlags(log.Lshortfile | log.LstdFlags)

	//strategy.Check()
	// 事件channel,10个缓存事件，如果当前有超过5个缓存了，这跳过价格推送
	priceEventStream := make(chan EventStream, 50)
	trainEventStream := make(chan EventStream, 50)
	// 开启标记价格推送
	go func() {
		for {
			doneCMarkPriceServe, _, err := futures.WsMarkPriceServe(strategy.Symbol, func(event *futures.WsMarkPriceEvent) {
				pushEvent := EventStream{EventType: "MarkPriceEvent", Event: event}
				priceEventStream <- pushEvent
				//log.Println("WsMarkPriceServe 推送成功")
			}, ErrHandler)

			if err != nil {
				log.Println(err)
				<-doneCMarkPriceServe
				time.Sleep(100 * time.Millisecond)
				continue
			}
			<-doneCMarkPriceServe
		}

	}()

	// 开启归集交易信息推送
	go func() {
		for {
			doneCAggTradeServe, _, err := futures.WsAggTradeServe(strategy.Symbol, func(event *futures.WsAggTradeEvent) {
				pushEvent := EventStream{EventType: "AggTradeEvent", Event: event}
				priceEventStream <- pushEvent
				//log.Println("AggTradeEvent 推送成功")
			}, ErrHandler)

			if err != nil {
				log.Println(err)
				<-doneCAggTradeServe
				time.Sleep(100 * time.Millisecond)
				continue
			}
			<-doneCAggTradeServe
		}
	}()
	go func() {
		for {
			// 创建一个Listenkey,订阅用户信息推送，20分钟重启一次
			listenkey, err := strategy.FutureClient.NewStartUserStreamService().Do(context.Background())
			if err != nil {
				log.Println(err)
				time.Sleep(100 * time.Millisecond)
				continue
			}
			startTime := time.Now()

			log.Println("启动账户信息推送，listenkey：", listenkey)
			var doneCUserDataServe chan struct{}
			doneCUserDataServe, _, err = binance.WsFutureUserDataServe(listenkey, func(event []byte) {
				// 一定时间延长一次
				if time.Now().After(startTime.Add(5 * time.Minute)) {
					strategy.FutureClient.NewKeepaliveUserStreamService().Do(context.Background())
					startTime = time.Now()
				}

				if gjson.Get(string(event), "e").String() == "ORDER_TRADE_UPDATE" {
					futureOrderTradeUpdateEvent := &FutureOrderTradeUpdateEvent{}
					json.Unmarshal([]byte(gjson.Get(string(event), "o").String()), &futureOrderTradeUpdateEvent)

					eventTime := gjson.Get(string(event), "E").Int()

					futureOrderTradeUpdateEvent.UpdateTime = time.Unix(int64(eventTime/1000), int64(eventTime%1000))

					// 只推送TRADE事件,而且是优先推送
					if futureOrderTradeUpdateEvent.OrderStatus == "FILLED" || futureOrderTradeUpdateEvent.OrderStatus == "PARTIALLY_FILLED" {
						pushEvent := EventStream{EventType: "OrderTradeUpdateEvent", Event: futureOrderTradeUpdateEvent}
						trainEventStream <- pushEvent
					}
				}

				if gjson.Get(string(event), "e").String() == "listenKeyExpired" {
					log.Println("listenKeyExpired过期，即将重启")
					doneCUserDataServe <- struct{}{}
				}

				//log.Println("FutureUserDataServe 推送成功")
			}, ErrHandler)

			if err != nil {
				log.Println(err)
				time.Sleep(100 * time.Millisecond)
				continue
			}
			<-doneCUserDataServe
		}
	}()

	var wg = &sync.WaitGroup{}
	wg.Add(1)

	// 开始处理推送
	go func() {
		for {
			//优先推送交易事件
			event := EventStream{}
			if len(trainEventStream) > 0 {
				event = <-trainEventStream
			} else {
				event = <-priceEventStream
			}

			if (len(priceEventStream) >= 2 || time.Now().Before(strategy.LastPriceEventTime.Add(time.Duration(strategy.PriceEventInterval)*time.Millisecond))) && (event.EventType == "MarkPriceEvent" || event.EventType == "AggTradeEvent") {
				continue
			} else {
				strategy.EventHandler(&event)
				if event.EventType == "MarkPriceEvent" || event.EventType == "AggTradeEvent" {
					strategy.LastPriceEventTime = time.Now()
				}

			}
		}
	}()
	go func() {
		strategy.KillWatch()
		wg.Done()
	}()
	wg.Wait()
}
