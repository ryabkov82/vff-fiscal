#!/usr/bin/env perl
use strict;
use warnings;

use FindBin qw($Bin);
use lib "$Bin/../lib";

use Test::More;

my $cgi_path = "$Bin/../srv_customlab_nalog.cgi";
open my $fh, '<', $cgi_path or die "cannot read $cgi_path: $!";
my $source = do { local $/; <$fh> };
close $fh;

my @forbidden_in_helper = qw(
    receipt_uuid
    receipt_link
    receiptUuid
    receiptLink
    receiptJsonLink
    print_url
    json_url
    inn
);

sub extract_sub {
    my ($name) = @_;
  my ($sub) = $source =~ /sub \Q$name\E \{(.*?)^\}/ms
    or die "sub $name not found";
    return $sub;
}

subtest 'public_success_response helper' => sub {
    my $helper = extract_sub('public_success_response');

    like(
        $helper,
        qr/return \{\s*status => 200,\s*msg => \$message,\s*\};/s,
        'returns only status and msg'
    );

    for my $field (@forbidden_in_helper) {
        unlike( $helper, qr/\Q$field\E/i, "helper does not reference $field" );
    }

    my @helper_calls = $source =~ /public_success_response\(\s*'([^']+)'\s*\)/g;
    is( scalar @helper_calls, 2, 'exactly two public success responses use helper' );
    is_deeply(
        [ sort @helper_calls ],
        [ 'Receipt already sent', 'Receipt created' ],
        'both receipt success messages use helper'
    );
};

subtest 'successful public responses omit receipt identifiers' => sub {
    my $send_receipt = extract_sub('send_receipt');

    unlike(
        $send_receipt,
        qr/return public_success_response\([^)]+\)[^;]*receipt_/s,
        'helper calls are not augmented with receipt fields'
    );

    unlike(
        $send_receipt,
        qr/return \{\s*status => 200,\s*msg => 'Receipt (?:created|already sent)'.*receipt_/s,
        'no inline success hash includes receipt fields'
    );

    unlike(
        $send_receipt,
        qr/public_success_response\([^)]+\)\s*=>/s,
        'success helper is not used as a hash value with extra keys'
    );
};

subtest 'metadata persistence remains unchanged' => sub {
    like( $source, qr/income_send => 1,/s, 'metadata still sets income_send' );
    like( $source, qr/receiptUuid => \$decoded->\{receipt_uuid\}/s,
        'metadata still stores receiptUuid from backend' );
    like( $source, qr/receiptLink => \$decoded->\{print_url\}/s,
        'metadata still stores receiptLink from backend' );
    like( $source, qr/receiptJsonLink => \$decoded->\{json_url\}/s,
        'metadata still stores receiptJsonLink from backend' );

    like(
        $source,
        qr/\$pay->set_json\('comment', \{.*?\}\);\s*\$shm->commit;/s,
        'commit follows metadata persistence'
    );

    like(
        $source,
        qr/\$shm->commit;\s*return public_success_response\('Receipt created'\);/s,
        'Receipt created response follows commit'
    );
};

subtest 'backend validation and error responses preserved' => sub {
    like(
        $source,
        qr/unless \(\$decoded && \$decoded->\{receipt_uuid\} && \$decoded->\{print_url\}\)/,
        'incomplete backend response validation remains'
    );

    like(
        $source,
        qr/return \{ status => 502, msg => 'VFF Fiscal returned an incomplete response' \};/,
        'incomplete backend response error unchanged'
    );

    like(
        $source,
        qr/return \{\s*status => \$response->code \|\| 502,\s*msg => 'VFF Fiscal request failed',\s*error =>/s,
        'backend failure responses still include error detail'
    );

    unlike(
        $source,
        qr/public_success_response\([^)]*error/s,
        'error paths do not use the success helper'
    );
};

done_testing;
