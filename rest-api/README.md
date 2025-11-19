# Sample invoke for pushing QR detail onchain

``` sh
curl -X POST 'http://localhost:3002/invoke' \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  --data 'channelid=mychannel' \
  --data 'chaincodeid=livestock' \
  --data 'function=CreateAsset' \
  --data-urlencode 'args@asset.json'
```

# Sample query for getting QR details

``` sh
curl 'http://localhost:3002/query?channelid=mychannel&chaincodeid=livestock&function=ReadAsset&args=2' 
```