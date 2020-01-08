import time
k = -0.05
new_amount = 0
contract = "quarter"
case = [0, 0, 0, 0, 0]
get_return_3 = 0

def volum():
    positionvolum = exchange.GetPosition()
    iniamount = positionvolum[0].Amount
    return iniamount

def initial():
    exchange.SetContractType("quarter")
    exchange.SetMarginLevel(20)
    iniposition = exchange.GetPosition()
    iniprice = iniposition[0].Price
    profitrate = iniposition[0].Info.profit_rate
    return profitrate

def depth(iniamount):
    exchange.SetContractType(contract)
    inidepth = exchange.GetDepth()
    iniask = inidepth.Asks[0].Price		# 卖二价
    inibid = inidepth.Bids[0].Price		# 买二价
    buyprice = round(iniask * (1-0.03 / 20), 2)
    sellprice = round(inibid * (1+0.03 / 20), 2)
    tradeamount = round(iniamount * 0.1)
    return iniask, inibid, buyprice, sellprice, tradeamount

def buytrade(buyprice, tradeamount):
	exchange.SetContractType(contract)
	exchange.SetMarginLevel(20)
	exchange.SetDirection("buy")
	exchange.Buy(buyprice, tradeamount)


def closebuy(iniask, tradeamount):
	exchange.SetContractType(contract)
	exchange.SetMarginLevel(20)
	exchange.SetDirection("closebuy")
	exchange.Sell(iniask, tradeamount)

def closesell(inibid, tradeamount):
	exchange.SetContractType(contract)
	exchange.SetMarginLevel(20)
	exchange.SetDirection("closesell")
	exchange.Buy(inibid, tradeamount)

def selltrade(sellprice, tradeamount):
	exchange.SetContractType(contract)
	exchange.SetMarginLevel(20)
	exchange.SetDirection("sell")
	exchange.Sell(sellprice, tradeamount)

def update_position():
    exchange.SetContractType(contract)
    new_position = exchange.GetPosition()
    new_amount = new_position[0].Amount
    return new_amount

def get_orders(buyprice, inibid, sellprice, iniask, profitrate):
    price = 0
    exchange.SetContractType(contract)
    iniposition = exchange.GetPosition()
    current_profitrate = iniposition[0].Info.profit_rate
    current_id = 0
    exchange.SetContractType(contract)
    orders = exchange.GetOrders()
    for x in orders:
        if x.Id:
            if x.Offset == 1 and x.Price == iniask and current_profitrate - -0.05 < -0.02:
                time.sleep(0.1)
                exchange.CancelOrder(x.Id)
                Log('return 2')
                return 2
            elif x.Offset == 1 and x.Price == iniask and current_profitrate < -0.05:
                time.sleep(0.1)
                Log('return 1')
                return 1
    if current_profitrate < -0.05:
        Log('return 4')
        return 4
    elif current_profitrate > -0.045:
        Log('return 3')
        return 3
    
    
def get_orders2(iniask, profitrate):
    price = 0
    exchange.SetContractType(contract)
    current_id = '0'
    orders = exchange.GetOrders()
    for x in orders:
        if x.Offset == 1 and x.Price == iniask:
            exchange.SetContractType(contract)
            iniposition = exchange.GetPosition()
            return x.Id
        else:
            return 0
    else:
        return 1

def diff_condition(amount, iniamount, profitrate):
    iniask, inibid, buyprice, sellprice, tradeamount = depth(iniamount)
    closebuy(iniask, tradeamount)
    temp_var = get_orders2(iniask, profitrate)
    while temp_var != 1:
        if temp_var == 0:
            iniask, inibid, buyprice, sellprice, tradeamount = depth(iniamount)
            closebuy(iniask, amount)
            Log('30%')
        else:
            time.sleep(0.5)
    reorderposition = exchange.GetPosition()
    reorderprice = reorderposition[0].Price
    buytrade(round(reorderprice),amount)
    Log('已挂补仓单')

def main():
    Log('策略运行成功，', '开始监控')
    iniamount = volum()
    Log('初始仓位:', str(iniamount))
    while 1:
        profitrate = initial()
        if profitrate < k:
            get_return_3 = 0
            Log('亏损率大于5%, 开始交易')
            iniask, inibid, buyprice, sellprice, tradeamount = depth(iniamount)
            closebuy(iniask, tradeamount)
            Log('平仓挂单成功，get orders')
            time.sleep(0.1)
            temp_var = get_orders(buyprice, inibid, sellprice, iniask, profitrate)
            while temp_var != 4:
                if temp_var == 2:
                    #执行第二步
                    iniask, inibid, buyprice, sellprice, tradeamount = depth(iniamount)
                    closebuy(iniask, tradeamount)
                    temp_var = get_orders(buyprice, inibid, sellprice, iniask, profitrate)
                    time.sleep(0.15)
                elif temp_var == 1:
                    temp_var = get_orders(buyprice, inibid, sellprice, iniask, profitrate)
                    time.sleep(0.15)
                elif temp_var == 3:
                    get_return_3 = 1
                    Log('收益率相同')
                    time.sleep(0.15)
            Log('平仓已成交')
            buytrade(buyprice,tradeamount)
            Log('开仓已挂单')
            time.sleep(0.15)
            new_amount = update_position()
            while int(new_amount) != int(iniamount):
                if get_return_3 == 1:
                    exchange.SetContractType(contract)
                    newo = exchange.GetOrders()
                    new_Id = newo[0].Id
                    time.sleep(0.15)
                    exchange.CancelOrder(new_Id)
                    iniask, inibid, buyprice, sellprice, tradeamount = depth(iniamount)
                    buytrade(inibid, tradeamount)
                    time.sleep(0.15)
                new_amount = update_position()
                time.sleep(0.2)
            Log('挂单已成交，卖出价：'+ str(iniask), '买入价：'+ str(buyprice), '成交量：'+ str(tradeamount), '当前持仓量: '+ str(new_amount))	# 输出'成功'
        elif profitrate >= 0.5 and profitrate < 0.8:
            if case[0] == 0:
                diff_condition(round(iniamount * 0.15), iniamount, profitrate)
                case[0] = 1
        elif profitrate >= 0.8 and profitrate < 1.1:
            if case[1] == 0:
                diff_condition(round(iniamount * 0.15), iniamount, profitrate)
                case[1] = 1
        elif profitrate >= 1.1 and profitrate < 1.5:
            if case[2] == 0:
                diff_condition(round(iniamount * 0.2), iniamount, profitrate)
                case[2] = 1
        elif profitrate >= 1.5 and profitrate < 1.8:
            if case[3] == 0:
                diff_condition(round(iniamount * 0.2), iniamount, profitrate)
                case[3] = 1
        elif profitrate >= 1.8:
            if case[4] == 0:
                diff_condition(round(iniamount * 0.3), iniamount, profitrate)
                case[4] = 1
        else:
            time.sleep(0.15)
