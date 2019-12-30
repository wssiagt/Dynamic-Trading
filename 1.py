def get_orders(buyprice,profitrate):
    price = 0
    exchange.SetContractType(contract)
    current_id = '0'
    orders = exchange.GetOrders()
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
