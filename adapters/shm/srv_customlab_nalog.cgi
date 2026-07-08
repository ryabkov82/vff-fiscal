#!/usr/bin/perl
use v5.14;
use strict;
use warnings;

use Core::Base;
use Core::Utils qw(parse_headers encode_json decode_json now);
use LWP::UserAgent ();
use HTTP::Request ();
use SHM qw(:all);

our %ARGS = parse_args();
my $shm = SHM->new(skip_check_auth => 1);
my $config_service = get_service('config', _id => 'pay_systems');

unless ($config_service) {
    print_json({ status => 400, msg => 'Error: config pay_systems not exists' });
    exit;
}

my $module_name = 'srv_customlab_nalog';
my $all_config = $config_service->get_data;
my %config = $all_config->{$module_name} ? %{ $all_config->{$module_name} } : ();

my $enabled = $config{enabled};
my $backend_url = $config{backend_url} || 'http://vff-fiscal:8080/v1/receipts';
my $service_name = $config{service_name} || 'Услуга доступа к VPN-сервису VPN for Friends';
my $api_token = $ENV{VFF_FISCAL_API_TOKEN} || '';
my $pay_systems = $config{pay_systems};
my @allowed_pay_systems = ref $pay_systems eq 'ARRAY' ? @$pay_systems : ($pay_systems ? ($pay_systems) : ());

unless ($api_token) {
    print_json({ status => 500, msg => 'Error: VFF_FISCAL_API_TOKEN is not configured' });
    exit;
}

if (($ARGS{action} || '') eq 'send') {
    my $pay_id = $ARGS{pay_id};
    unless ($pay_id) {
        print_json({ status => 400, msg => 'Error: pay_id required' });
        exit;
    }
    unless ($enabled) {
        print_json({ status => 400, msg => "Error: $module_name not enabled" });
        exit;
    }

    my $result = send_receipt($pay_id);
    print_json($result);
    exit;
}

print_json({
    status => 200,
    msg => 'VFF Fiscal SHM adapter',
    enabled => $enabled ? 'true' : 'false',
});
exit;

sub send_receipt {
    my ($pay_id) = @_;
    my $pay = get_service('pay', _id => $pay_id);
    unless ($pay && $pay->id) {
        return { status => 404, msg => "Payment not found: $pay_id" };
    }

    my %payment = $pay->get;
    if ($payment{comment} && $payment{comment}{income_send}) {
        return {
            status => 200,
            msg => 'Receipt already sent',
            receipt_uuid => $payment{comment}{receiptUuid},
            receipt_link => $payment{comment}{receiptLink},
        };
    }

    if (@allowed_pay_systems && !grep { $_ eq $payment{pay_system_id} } @allowed_pay_systems) {
        return { status => 200, msg => 'Skipped: payment system mismatch' };
    }

    my $amount;
    if ($payment{comment}
        && $payment{comment}{object}
        && $payment{comment}{object}{amount}
        && $payment{comment}{object}{amount}{value}) {
        $amount = $payment{comment}{object}{amount}{value};
    } else {
        $amount = $payment{money};
    }
    unless ($amount && $amount > 0) {
        return { status => 400, msg => 'Error: payment amount is zero or negative' };
    }

    my $payload = {
        external_id => "shm:$pay_id",
        amount => sprintf('%.2f', $amount),
        service_name => $service_name,
    };

    my $request = HTTP::Request->new(POST => $backend_url);
    $request->header('Content-Type' => 'application/json');
    $request->header('Authorization' => "Bearer $api_token");
    $request->content(encode_json($payload));

    my $ua = LWP::UserAgent->new(timeout => 45);
    my $response = $ua->request($request);
    my $decoded;
    eval { $decoded = decode_json($response->decoded_content || '{}'); };

    unless ($response->is_success) {
        return {
            status => $response->code || 502,
            msg => 'VFF Fiscal request failed',
            error => ($decoded && $decoded->{error}) || $response->status_line,
        };
    }
    unless ($decoded && $decoded->{receipt_uuid} && $decoded->{print_url}) {
        return { status => 502, msg => 'VFF Fiscal returned an incomplete response' };
    }

    $pay->set_json('comment', {
        income_send => 1,
        receiptUuid => $decoded->{receipt_uuid},
        receiptLink => $decoded->{print_url},
        receiptJsonLink => $decoded->{json_url},
    });
    $shm->commit;

    return {
        status => 200,
        msg => 'Receipt created',
        receipt_uuid => $decoded->{receipt_uuid},
        receipt_link => $decoded->{print_url},
    };
}
