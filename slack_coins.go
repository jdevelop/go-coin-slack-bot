/*

mybot - Illustrative Slack bot in Go

Copyright (c) 2015 RapidLoop

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in
all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
THE SOFTWARE.
*/

package main

import (
	"fmt"
	"log"
	"os"
	"strings"
	"io/ioutil"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"
	"math"
	"github.com/boltdb/bolt"
)

type TickersPipeline interface {
	FetchCoins(symbol string) ([]TickerData, error)
}

type coinMarket struct{}

func makeCoinMarket() TickersPipeline {
	return &coinMarket{}
}

var mkt TickersPipeline

type Watch struct {
	Channel   string  `json:"channel"`
	Name      string  `json:"name"`
	lastPrice float64 `json:"last_price"`
	Threshold int     `json:"threshold"`
}

type WatchList map[string]*Watch

var watchList WatchList

func init() {
	mkt = makeCoinMarket()
	watchList = make(WatchList)
}

type TickerData struct {
	Id               string  `json:"id"`
	Name             string  `json:"name"`
	Symbol           string  `json:"symbol"`
	Rank             int16   `json:"rank,string"`
	PriceUSD         float64 `json:"price_usd,string"`
	PriceBTC         float64 `json:"price_btc,string"`
	Volume24H        float64 `json:"24h_volume_usd,string"`
	MarketCapUSD     float64 `json:"market_cap_usd,string"`
	AvailableSupply  float64 `json:"available_supply,string"`
	TotalSupply      float64 `json:"total_supply,string"`
	PercentChange1H  float32 `json:"percent_change_1h,string"`
	PercentChange24H float32 `json:"percent_change_24h,string"`
	PercentChange7D  float32 `json:"percent_change_7d,string"`
	LastUpdated      int32   `json:"last_updated,string"`
}

func LogError(err error) {
	fmt.Println(err)
}

func (mkt *coinMarket) FetchCoins(coinCode string) (result []TickerData, err error) {
	url := "https://api.coinmarketcap.com/v1/ticker"
	if coinCode != "" {
		url = fmt.Sprintf("%s/%s", url, coinCode)
	}
	var resp *http.Response
	if resp, err = http.Get(url); err != nil {
		LogError(err)
		return
	}
	defer resp.Body.Close()
	var body []byte
	if body, err = ioutil.ReadAll(resp.Body); err != nil {
		LogError(err)
		return
	}
	if err = json.Unmarshal(body, &result); err != nil {
		LogError(err)
		return
	}
	return
}

func main() {

	bucketName := []byte("watchlist")

	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: mybot slack-bot-token")
		os.Exit(1)
	}

	db, err := bolt.Open("coins.db", 0600, nil)
	if err != nil {
		log.Fatal(err)
	}

	// create storage
	err = db.Update(func(tx *bolt.Tx) (err error) {
		_, err = tx.CreateBucketIfNotExists(bucketName)
		return
	})

	if err != nil {
		log.Fatal(err)
	}

	err = db.View(func(tx *bolt.Tx) (err error) {
		bucket := tx.Bucket(bucketName)
		c := bucket.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var w Watch
			if err = json.Unmarshal(v, &w); err == nil {
				fmt.Println("Loaded", w)
				watchList[string(k)] = &w
			} else {
				fmt.Println(err)
			}
		}
		return
	})

	if err != nil {
		log.Fatal(err)
	}

	// start a websocket-based Real Time API session
	ws, id := slackConnect(os.Args[1])
	fmt.Println("My ID: ", id)

	defer db.Close()

	fmt.Println("mybot ready, ^C exits")

	helpF := func(m Message) {
		m.Text = "Commands: \n" +
			"*coin <symbol symbol ...>* - get coin stats \n" +
			"*rank <n>* - get top N records sorted by price\n" +
			"*watch symbol threshold* - watch coin price change\n" +
			"*unwatch symbol* - unwatch coin\n" +
			"*watchlist* - list current watched coins"
		postMessage(ws, m)
	}

	msgF := func(m Message) {
		recv := func() {
			if r := recover(); r != nil {
				go helpF(m)
			}
		}
		defer recv()
		if m.Type == "message" && strings.HasPrefix(m.Text, "<@"+id+">") {
			// if so try to parse if
			parts := strings.Fields(m.Text)
			switch parts[1] {
			case "watch":
				if len(parts) != 4 {
					helpF(m)
				} else if r, err := strconv.Atoi(parts[3]); err == nil && r > 0 {
					parts[2] = strings.ToLower(parts[2])
					if v, err := mkt.FetchCoins(""); err == nil {
						var coin *TickerData
						for _, c := range v {
							if strings.ToLower(c.Symbol) == parts[2] {
								coin = &c
								break
							}
						}
						if coin != nil {
							w := Watch{
								Channel:   m.Channel,
								Name:      coin.Name,
								lastPrice: coin.PriceUSD,
								Threshold: r,
							}
							watchList[parts[2]] = &w
							err = db.Update(func(tx *bolt.Tx) (err error) {
								if res, err := json.Marshal(&w); err == nil {
									fmt.Println(string(res))
									bucket := tx.Bucket(bucketName)
									err = bucket.Put([]byte(strings.ToLower(coin.Symbol)), res)
								}
								return
							})
							if err != nil {
								fmt.Println(err)
							}
						} else {
							m.Text = "Coin '" + parts[2] + "' does not exist"
							postMessage(ws, m)
						}
					} else {
						m.Text = "Cannot fetch coins"
						postMessage(ws, m)
					}
				} else {
					m.Text = "Cannot parse threshold " + parts[3]
					postMessage(ws, m)
				}
			case "unwatch":
				delete(watchList, parts[2])
				if err = db.Update(func(tx *bolt.Tx) (err error) {
					bucket := tx.Bucket(bucketName)
					err = bucket.Delete([]byte(parts[2]))
					return
				}); err != nil {
					fmt.Println(err)
				}
			case "watchlist":
				msg := "Tickers: [ "
				for k, v := range watchList {
					msg = msg + fmt.Sprintf("{ %1s:%2d }", k, v.Threshold)
				}
				m.Text = msg + "]"
				postMessage(ws, m)
			case "coin":
				// looks good, get the quote and reply with the result
				go func(m Message) {
					defer recv()
					m.Text = getQuote(parts[2:])
					postMessage(ws, m)
				}(m)
			case "rank":
				r, err := strconv.Atoi(parts[2])
				if err != nil {
					r = 10
				}
				go func(m Message) {
					m.Text = rankCoins(r)
					postMessage(ws, m)
				}(m)
			default:
				go helpF(m)
			}
			// NOTE: the Message object is copied, this is intentional
		}
	}

	loop := func() {
		defer func() {
			if r := recover(); r != nil {
				log.Fatal(r)
			}
		}()
		m, err := getMessage(ws)
		if err != nil {
			log.Fatal(err)
		}
		msgF(m)
	}

	ticker := time.NewTicker(2 * time.Minute)

	go func() {
		for range ticker.C {
			for key, sym := range watchList {
				fmt.Printf("Check %1s : %2s\n", key, sym.Name)
				v, err := mkt.FetchCoins(sym.Name)
				if err != nil {
					fmt.Printf("Skipping %1s\n", key)
					continue
				}
				if v != nil && sym.lastPrice > 0 {
					delta := (v)[0].PriceUSD - sym.lastPrice
					fmt.Printf("Delta %1s - %2.2f, threshold %3d\n", key, delta, sym.Threshold)
					if math.Abs(delta) > float64(sym.Threshold) {
						postMessage(ws, Message{
							Channel: sym.Channel,
							Type:    "message",
							Text:    fmt.Sprintf("[ %1s ] $%+2.2f to $%3.2f", (v)[0].Symbol, delta, (v)[0].PriceUSD),
						})
					}
				}
				sym.lastPrice = (v)[0].PriceUSD
			}
		}
	}()

	for {
		loop()
	}
}

// Get the quote via Yahoo. You should replace this method to something
// relevant to your team!
func getQuote(sym []string) string {

	set := make(map[string]bool)
	for _, v := range sym {
		set[strings.ToLower(v)] = true
	}

	coins, err := mkt.FetchCoins("")
	if err == nil && coins != nil && len(coins) > 0 {
		var resp string
		found := false
		for _, coin := range coins {
			if _, ok := set[strings.ToLower(coin.Symbol)]; ok {
				found = true
				resp = resp + fmt.Sprintf("%1s $%2.2f, update 1H: %3.2f%%, update 24H: %4.2f%%\n",
					coin.Symbol, coin.PriceUSD, coin.PercentChange1H, coin.PercentChange24H)
			}
		}
		if found {
			return resp
		} else {
			return fmt.Sprintf("'%1v' not found", sym)
		}
	} else {
		return "n/a"
	}

}

func rankCoins(limit int) string {
	min := func(l, r int) int {
		if l < r {
			return l
		} else {
			return r
		}
	}

	coins, err := mkt.FetchCoins("")
	if err == nil && coins != nil && len(coins) > 0 {
		var resp string
		sort.Slice(coins, func(l, r int) bool {
			return (coins)[l].MarketCapUSD > (coins)[r].MarketCapUSD
		})
		for _, v := range (coins)[:min(limit, 30)] {
			resp = resp + fmt.Sprintf("%1s : %2s => price: $%3.2f : market: $%4.2f\n", v.Name, v.Symbol, v.PriceUSD, v.MarketCapUSD)
		}
		return resp
	} else {
		return "n/a"
	}

}