#!/usr/bin/env perl
use strict;
use warnings;

use FindBin qw($Bin);
use File::Spec;
use File::Temp qw(tempfile);
use Test::More;

use lib File::Spec->catdir( $Bin, '..', 'perl', 'lib' );
use SHM ();
use Core::Config ();

my $REPO_ROOT = File::Spec->catdir( $Bin, '..', '..', '..' );
my $SCRIPT    = File::Spec->catfile(
    $REPO_ROOT,
    'ansible/roles/vff_fiscal_adapter/files/shm-auth-smoke.pl'
);
my $PERL5LIB = join ':',
    File::Spec->catdir( $Bin, '..', 'perl', 'lib' ),
    File::Spec->catdir( $REPO_ROOT, 'adapters/shm/lib' );

ok( -f $SCRIPT, 'shm-auth-smoke.pl exists for behavioral testing' );

my $SCRIPT_SOURCE = do {
    open my $fh, '<', $SCRIPT or die "cannot read $SCRIPT: $!";
    local $/; <$fh>;
};

sub reset_stub_state {
    SHM::reset_state();
    Core::Config::reset_state();
    delete $ENV{SHM_TEST_HTTP_STATUS};
    delete $ENV{SHM_TEST_HTTP_BODY};
}

sub run_auth_smoke {
    my (%env) = @_;
    reset_stub_state();

    my ( $state_fh, $state_file ) = tempfile( UNLINK => 1 );
    close $state_fh;

    local $ENV{PERL5LIB}             = $PERL5LIB;
    local $ENV{SHM_TEST_STATE_FILE}  = $state_file;
    local $ENV{VFF_FISCAL_API_TOKEN} = 'environment-token-value';
    local $ENV{SHM_TEST_HTTP_STATUS} = $env{http_status} if exists $env{http_status};
    local $ENV{SHM_TEST_HTTP_BODY}   = $env{http_body} if exists $env{http_body};

    my $output = `"$^X" "$SCRIPT" 2>&1`;
    my $exit   = $? >> 8;
    my @events = SHM::read_recorded_events($state_file);
    return ( $output, $exit, \@events );
}

sub assert_init_before_get_service {
    my ($events) = @_;
    my ( $init_index, $get_service_index, $http_index );

    for my $idx ( 0 .. $#{$events} ) {
        $init_index        = $idx if $events->[$idx] eq 'shm_initialized';
        $get_service_index = $idx if $events->[$idx] =~ /\Aget_service init=1\z/;
        $http_index        = $idx if $events->[$idx] eq 'http_request';
    }

    ok( defined $init_index, 'subprocess initialized SHM' );
    ok( defined $get_service_index, 'subprocess reached get_service' );
    ok( defined $http_index, 'subprocess issued HTTP request' );
    ok(
        defined $init_index
            && defined $get_service_index
            && $init_index < $get_service_index,
        'SHM initialization happened before get_service'
    );
    ok(
        defined $get_service_index
            && defined $http_index
            && $get_service_index < $http_index,
        'HTTP request happened after get_service'
    );
}

subtest 'auth smoke initializes SHM before config access' => sub {
    like(
        $SCRIPT_SOURCE,
        qr/my \$shm = SHM->new\(skip_check_auth => 1\);\s*my \$config_service = get_service\('config', _id => 'pay_systems'\)/s,
        'SHM context is created before get_service'
    );
    unlike( $SCRIPT_SOURCE, qr/get_service\('config'.*SHM->new/s,
        'get_service is not called before SHM initialization' );
};

subtest 'auth smoke success output remains HTTP 200 only' => sub {
    my ( $output, $exit, $events ) = run_auth_smoke();
    is( $exit, 0, 'auth smoke exits successfully on HTTP 200' );
    is( $output, "HTTP 200\n", 'success output is exactly HTTP 200' );

    assert_init_before_get_service($events);

    unlike( $output, qr/secret-user-must-not-appear-in-output/,
        'response body is not printed' );
    unlike( $output, qr/environment-token-value/,
        'API token is not printed' );
    unlike( $output, qr/Authorization/i, 'authorization header is not printed' );
};

subtest 'auth smoke failure does not expose secrets' => sub {
    my ( $output, $exit ) = run_auth_smoke(
        http_status => 401,
        http_body   => '{"error":"secret backend failure"}',
    );
    isnt( $exit, 0, 'non-2xx HTTP response fails' );
    unlike( $output, qr/secret backend failure/, 'response body is not printed on failure' );
    unlike( $output, qr/environment-token-value/, 'API token is not printed on failure' );
};

done_testing();
