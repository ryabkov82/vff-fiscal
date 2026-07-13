#!/usr/bin/env perl
use v5.14;
use strict;
use warnings;

use lib '/app/data/pay_systems/lib';

use Core::Base;
use Core::Utils qw(decode_json);
use SHM qw(:all);
use LWP::UserAgent ();
use HTTP::Request ();
use VFFFiscal::AdapterConfig qw(resolve_api_token normalize_non_empty_scalar);

my $MODULE = 'srv_customlab_nalog';

sub fail {
    die shift(@_) . "\n";
}

my $shm = SHM->new(skip_check_auth => 1);

my $config_service = get_service('config', _id => 'pay_systems')
    or fail('pay_systems config service is missing');
my $all_config = $config_service->get_data
    or fail('pay_systems config data is missing');
my %cfg = ref $all_config->{$MODULE} eq 'HASH'
    ? %{ $all_config->{$MODULE} }
    : ();

my $api_token = resolve_api_token($cfg{client_token}, $ENV{VFF_FISCAL_API_TOKEN});
fail('client_token or VFF_FISCAL_API_TOKEN is not configured')
    unless defined $api_token && length $api_token;

my $backend_url = normalize_non_empty_scalar($cfg{backend_url})
    // 'http://vff-fiscal:8080/v1/receipts';
my ($base_url) = $backend_url =~ /\A(.+\/v1)\/receipts\z/
    or fail('backend_url is invalid');
my $user_url = "$base_url/user";

my $request = HTTP::Request->new(GET => $user_url);
$request->header('Accept' => 'application/json');
$request->header('Authorization' => "Bearer $api_token");

my $response = LWP::UserAgent->new(timeout => 30)->request($request);
fail('authenticated user request failed') unless $response->is_success;

print "HTTP 200\n";
