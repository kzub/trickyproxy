var request = require('request')
var async = require('async')

var url = 'http://localhost:8036/riak/test/key';
var count = 0;
function rq(n, cb){
        request(url+count, cb);
        count++;
        console.log(count);
}

async.timesLimit(1000, 50, rq, function(err){
  console.log('DONE', err||'');
  console.log('count:' + count);
});