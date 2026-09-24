#!/usr/bin/env perl
use strict;
use warnings;

use FindBin qw($Bin);
use File::Temp qw(tempdir);
use JSON::PP qw(encode_json decode_json);
use Test::More;

my $cgi = "$Bin/../srv_customlab_nalog.cgi";
my $lib = tempdir( CLEANUP => 1 );

sub write_file {
    my ( $path, $body ) = @_;
    open my $fh, '>', $path or die $!;
    print {$fh} $body;
    close $fh;
}

mkdir "$lib/Core" or die $!;
mkdir "$lib/LWP"  or die $!;
mkdir "$lib/HTTP" or die $!;

write_file(
    "$lib/Core/Base.pm",
    "package Core::Base;\nuse strict;\nuse warnings;\n1;\n"
);
write_file(
    "$lib/Core/Utils.pm",
    <<'PERL'
package Core::Utils;
use strict;
use warnings;
use JSON::PP ();
use Exporter qw(import);
our @EXPORT_OK = qw(parse_headers encode_json decode_json now parse_args print_json);
sub parse_headers { return {} }
sub encode_json { JSON::PP::encode_json($_[0]) }
sub decode_json { JSON::PP::decode_json($_[0]) }
sub now { return '2026-09-24T00:00:00Z' }
sub parse_args {
    my %args;
    for my $item (@ARGV) {
        my ( $key, $value ) = split /=/, $item, 2;
        $args{$key} = $value if defined $key;
    }
    return %args;
}
sub print_json {
    print JSON::PP::encode_json($_[0]), "\n";
}
1;
PERL
);
write_file(
    "$lib/HTTP/Request.pm",
    <<'PERL'
package HTTP::Request;
use strict;
use warnings;
sub new {
    my ( $class, $method, $url ) = @_;
    return bless { method => $method, url => $url, headers => {}, content => '' }, $class;
}
sub header {
    my ( $self, $key, $value ) = @_;
    $self->{headers}{$key} = $value if defined $value;
    return $self->{headers}{$key};
}
sub content {
    my ( $self, $value ) = @_;
    $self->{content} = $value if defined $value;
    return $self->{content};
}
1;
PERL
);
write_file(
    "$lib/HTTP/Response.pm",
    <<'PERL'
package HTTP::Response;
use strict;
use warnings;
sub new {
    my ( $class, $code, $msg, $headers, $content ) = @_;
    return bless { code => $code, msg => $msg // '', content => $content // '' }, $class;
}
sub is_success { $_[0]->{code} >= 200 && $_[0]->{code} < 300 }
sub code { $_[0]->{code} }
sub status_line { "$_[0]->{code} $_[0]->{msg}" }
sub decoded_content { $_[0]->{content} }
1;
PERL
);
write_file(
    "$lib/LWP/UserAgent.pm",
    <<'PERL'
package LWP::UserAgent;
use strict;
use warnings;
use HTTP::Response ();
use JSON::PP ();
sub new { bless {}, $_[0] }
sub request {
    my ( $self, $request ) = @_;
    my $log = $ENV{SHM_TEST_HTTP_LOG} or die "missing http log\n";
    open my $fh, '>>', $log or die $!;
    print {$fh} JSON::PP::encode_json({
        url => $request->{url},
        content => $request->{content},
        authorization => $request->{headers}{Authorization} ? 1 : 0,
    }), "\n";
    close $fh;
    my $body = $ENV{SHM_TEST_HTTP_BODY}
        // '{"receipt_uuid":"rcpt-secret","print_url":"https://example.test/print","json_url":"https://example.test/json"}';
    return HTTP::Response->new(200, 'OK', [], $body);
}
1;
PERL
);
write_file(
    "$lib/SHM.pm",
    <<'PERL'
package SHM;
use strict;
use warnings;
use JSON::PP ();
use Exporter qw(import);
our @EXPORT = qw(get_service);
our %EXPORT_TAGS = ( all => [@EXPORT] );
our $PAYMENT;
our $COMMENT_UPDATES = 0;
sub new { return bless {}, $_[0] }
sub commit { return 1 }
sub get_service {
    my ( $name, %args ) = @_;
    if ( $name eq 'config' ) {
        return bless {}, 'SHM::Config';
    }
    if ( $name eq 'pay' ) {
        return unless $PAYMENT && ( $args{_id} // 0 ) == $PAYMENT->{id};
        return bless { id => $PAYMENT->{id} }, 'SHM::Pay';
    }
    return;
}
package SHM::Config;
sub get_data {
    return {
        srv_customlab_nalog => {
            enabled => 1,
            client_token => 'secret-token',
            backend_url => 'http://fiscal.test/v1/receipts',
            service_name => 'VPN',
            pay_systems => [ 'yookassa', 'platega' ],
        },
    };
}
package SHM::Pay;
sub id { $_[0]->{id} }
sub get {
    my %copy = %{$SHM::PAYMENT};
    $copy{comment} = { %{ $SHM::PAYMENT->{comment} } };
    return %copy;
}
sub set_json {
    my ( $self, $field, $value ) = @_;
    $SHM::COMMENT_UPDATES++;
    $SHM::PAYMENT->{comment}{$_} = $value->{$_} for keys %{$value};
    return 1;
}
1;
PERL
);

sub run_send {
    my ($payment) = @_;
    my $state = tempdir( CLEANUP => 1 );
    my $payment_file = "$state/payment.json";
    my $http_log = "$state/http.log";
    write_file( $payment_file, encode_json($payment) );
    my $wrapper = "$state/run.pl";
    write_file(
        $wrapper,
        <<"PERL"
use strict;
use warnings;
use JSON::PP qw(decode_json);
use lib '$lib';
use Core::Utils qw(parse_args print_json);
use SHM ();
open my \$fh, '<', '$payment_file' or die \$!;
local \$/;
\$SHM::PAYMENT = decode_json(<\$fh>);
close \$fh;
do '$cgi' or die \$@ || \$!;
PERL
    );
    my $adapter_lib = "$Bin/../lib";
    local $ENV{SHM_TEST_HTTP_LOG} = $http_log;
    my $output = `perl -I$adapter_lib -I$lib $wrapper action=send pay_id=$payment->{id} 2>&1`;
    my $exit = $? >> 8;
    my @http;
    if ( -f $http_log ) {
        open my $fh, '<', $http_log or die $!;
        while ( my $line = <$fh> ) {
            chomp $line;
            push @http, decode_json($line) if length $line;
        }
        close $fh;
    }
    my $json;
    if ( $output =~ /(\{.*\})\s*\z/s ) {
        $json = eval { decode_json($1) };
    }
    return ( $exit, $json, \@http, $output );
}

my $yookassa = {
    id => 77,
    pay_system_id => 'yookassa',
    user_id => 19,
    money => '10.00',
    comment => {
        object => {
            paid => 1,
            status => 'succeeded',
            captured_at => '2026-07-08T10:48:55Z',
            amount => { value => '10.00', currency => 'RUB' },
        },
    },
};

subtest 'yookassa external_id and captured_at' => sub {
    my ( $exit, $json, $http, $output ) = run_send($yookassa);
    is( $exit, 0, 'exit 0' ) or diag $output;
    is( $json->{status}, 200, 'created' );
    is( $json->{msg}, 'Receipt created', 'message' );
    is( scalar @$http, 1, 'one backend call' );
    my $body = decode_json( $http->[0]{content} );
    is( $body->{external_id}, 'shm:77', 'external_id' );
    is( $body->{amount}, '10.00', 'amount' );
    is( $body->{operation_time}, '2026-07-08T10:48:55Z', 'captured_at' );
    unlike( $output, qr/secret-token|rcpt-secret/, 'no secrets in output' );
};

subtest 'existing income_send is idempotent' => sub {
    my %payment = %{$yookassa};
    $payment{comment} = { %{ $yookassa->{comment} }, income_send => 1, receiptUuid => 'rcpt-secret' };
    my ( $exit, $json, $http, $output ) = run_send(\%payment);
    is( $json->{msg}, 'Receipt already sent', 'idempotent message' );
    is( scalar @$http, 0, 'no backend call' );
    unlike( $output, qr/rcpt-secret/, 'uuid not echoed' );
};

subtest 'platega service amount ignores provider total' => sub {
    my ( $exit, $json, $http, $output ) = run_send({
        id => 77,
        pay_system_id => 'platega',
        user_id => 19,
        money => '150.00',
        comment => {
            provider => 'platega',
            transaction_id => '11111111-1111-1111-1111-111111111111',
            status => 'CONFIRMED',
            provider_status => 'CONFIRMED',
            amount => '150.00',
            amount_kopecks => 15000,
            currency => 'RUB',
            brand_id => 'vff',
            payload => 'vpnbot:v2:vff:19:15000',
            observed_at => '2026-09-24T15:04:05Z',
            provider_amount => '162.00',
            provider_commission => '12.00',
        },
    });
    is( $json->{msg}, 'Receipt created', 'created' ) or diag $output;
    my $body = decode_json( $http->[0]{content} );
    is( $body->{amount}, '150.00', 'service amount' );
    is( $body->{external_id}, 'shm:77', 'external_id' );
    is( $body->{operation_time}, '2026-09-24T15:04:05Z', 'observed_at' );
};

subtest 'chargebacked platega payment is skipped' => sub {
    my ( $exit, $json, $http ) = run_send({
        id => 77,
        pay_system_id => 'platega',
        user_id => 19,
        money => '150.00',
        comment => {
            provider => 'platega',
            status => 'CHARGEBACKED',
            provider_status => 'CONFIRMED',
            amount => '150.00',
            amount_kopecks => 15000,
        },
    });
    is( $json->{status}, 200, 'skip status' );
    like( $json->{msg}, qr/chargebacked/, 'skip message' );
    is( scalar @$http, 0, 'no backend call' );
};

done_testing;
