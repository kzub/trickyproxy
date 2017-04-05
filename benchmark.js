var request = require('request')
var async = require('async')

var url = 'http://localhost:8037/riak/test/key';

var responses = {}
function rq(n, cb){
	request(url + n, function(err, resp){
		if(resp){
			if(responses[resp.statusCode] === undefined){
				responses[resp.statusCode] = 0;
			}
			responses[resp.statusCode]++;
		}
		console.log(n, ">", resp && resp.statusCode)
		cb(err);
	});
}

async.timesLimit(1000, 50, rq, function(err){
	console.log('DONE', err, responses);
});