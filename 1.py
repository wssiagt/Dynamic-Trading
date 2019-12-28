import time
k = -0.05
new_amount = 0
new_amount1 = 0
new_profit = 0
contract = "quarter"
case = [0, 0, 0, 0, 0]

def volum():
    positionvolum = exchange.GetPosition()
    iniamount = positionvolum[1].Amount
    return iniamount

def initial():
    exchange.SetContractType("quarter")
    exchange.SetMarginLevel(20)
    iniposition = exchange.GetPosition()
    iniprice = iniposition[1].Price
    profitrate = iniposition[1].Info.profit_rate
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
    new_position = exchange.GetPosition()
    new_amount = new_position[1].Amount
    new_amount1 = new_position[1].Amount
    return new_amount, new_amount1

def get_orders(buyprice,profitrate):
    price = 0
    exchange.SetContractType(contract)
    current_id = '0'
    orders = exchange.GetOrders();
    for x in orders:
        if x.Offset == 0 and x.Price == buyprice:
            iniposition = exchange.GetPosition()
            if iniposition[1].Info.profit_rate - profitrate >= -0.02:
                return x.Id
            else:
                current_id = x.Id
                break
        else:
            continue
    if current_id != '0':
        exchange.CancelOrder(id)
        return 0
    return 1

def diff_condition(amount, iniamount, profitrate):
    iniask, inibid, buyprice, sellprice, tradeamount = depth(iniamount)
    closesell(inibid, tradeamount)
    temp_var = get_orders(buyprice,profitrate)
    while temp_var != 1:
        if temp_var == 0:
            iniask, inibid, buyprice, sellprice, tradeamount = depth(iniamount)
            closesell(inibid, amount)
        else:
            time.sleep(0.5)
    reorderposition = exchange.GetPosition()
    reorderprice = reorderposition[1].Price
    selltrade(round(reorderprice),amount)

def main():
    Log('策略运行成功，', '开始监控')
    while 1:
        iniamount = volum()
        profitrate = initial()
        if profitrate < k:
            Log('亏损率大于5%', '初始仓位：'+ str(iniamount), '开始交易')
            iniask, inibid, buyprice, sellprice, tradeamount = depth(iniamount)
            closesell(inibid, tradeamount)
            temp_var = get_orders(buyprice,profitrate)
            while temp_var != 1:
                if temp_var == 0:
                    iniask, inibid, buyprice, sellprice, tradeamount = depth(iniamount)
                    closesell(inibid, tradeamount)
                else:
                    time.sleep(0.5)
            Log('平仓已成交')
            selltrade(sellprice,tradeamount)														# 否则，不相等（平仓成交）：
            Log('开仓已挂单')
            new_amount, new_amount1 = update_position()
            while int(new_amount1) != int(iniamount):		# new_amount1与iniamount是否相等
                new_amount, new_amount1 = update_position()
                time.sleep(0.3)						# 不相等，执行休眠
            Log('挂单已成交，卖出价：'+ str(iniask), '买入价：'+ str(buyprice), '成交量：'+ str(tradeamount), '当前持仓量: '+ str(new_amount1))	# 输出'成功'
        elif profitrate >= 0.3 and profitrate < 0.6:
            if case[0] == 0:
                diff_condition(round(iniamount * 0.15), iniamount, profitrate)
                case[0] = 1
        elif profitrate >= 0.6 and profitrate < 0.9:
            if case[1] == 0:
                diff_condition(round(iniamount * 0.15), iniamount, profitrate)
                case[1] = 1
        elif profitrate >= 0.9 and profitrate < 1.2:
            if case[2] == 0:
                diff_condition(round(iniamount * 0.2), iniamount, profitrate)
                case[2] = 1
        elif profitrate >= 1.2 and profitrate < 1.5:
            if case[3] == 0:
                diff_condition(round(iniamount * 0.2), iniamount, profitrate)
                case[3] = 1
        elif profitrate >= 1.5:
            if case[4] == 0:
                diff_condition(round(iniamount * 0.3), iniamount, profitrate)
                case[4] = 1
        else:
            time.sleep(0.2)
