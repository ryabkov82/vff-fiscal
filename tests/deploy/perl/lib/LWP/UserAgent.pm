package LWP::UserAgent;

use strict;
use warnings;

use HTTP::Response ();

sub new {
    my ($class) = @_;
    return bless {}, $class;
}

sub request {
    my ( $self, $request ) = @_;
    require SHM;
    SHM::_record_event('http_request');

    my $status = $ENV{SHM_TEST_HTTP_STATUS} // 200;
    my $body   = $ENV{SHM_TEST_HTTP_BODY}
        // '{"user":"secret-user-must-not-appear-in-output"}';

    return HTTP::Response->new( $status, 'Stubbed', [], $body );
}

1;
